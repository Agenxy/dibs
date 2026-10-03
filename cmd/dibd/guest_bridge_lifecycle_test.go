package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGuestBridgeNeverFollowsRedirectsOrDowngradesTLS(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", bin, "../dibs").CombinedOutput(); err != nil {
		t.Fatalf("build actual bridge: %v %s", err, out)
	}
	config, pem, pin, err := guestTLS(t.TempDir(), netip.MustParseAddr("::1"))
	if err != nil {
		t.Fatal(err)
	}
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	for _, status := range []int{301, 302, 303, 307, 308, 0} {
		var hits atomic.Int64
		maxTLS := uint16(tls.VersionTLS13)
		if status == 0 {
			maxTLS = tls.VersionTLS12
		}
		key := "dibs_inv_" + strings.Repeat("a", 64)
		endpoint := guestReplyFixture(t, config.Certificates[0], maxTLS, http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("X-Dibs-Local") != "" ||
					r.Header.Get("X-Dibs-Agent-Nonce") != "" || r.Method != http.MethodPost {
					t.Error("guest leaked/substituted local credentials or used a non-POST request")
				}
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
			}))
		file := guestCommandRecipe(t, endpoint, key, pem, pin)
		out := guestCommandOutput(t, bin, file, []byte(`{"jsonrpc":"2.0","id":23,"method":"tools/list"}`))
		var reply struct {
			ID    int `json:"id"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal(out, &reply); err != nil || reply.ID != 23 || reply.Error == nil {
			t.Fatalf("redirect/TLS failure did not answer the caller: %v", err)
		}
		want := int64(1)
		if status == 0 {
			want = 0
		}
		if hits.Load() != want {
			t.Fatalf("redirect replayed or TLS1.2 reached HTTP: status=%d hits=%d", status, hits.Load())
		}
	}
	if targetHits.Load() != 0 {
		t.Fatal("a redirect target received a request or credential")
	}
}

func TestGuestBridgeInvitationIsStartupSnapshot(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", bin, "../dibs").CombinedOutput(); err != nil {
		t.Fatalf("build actual bridge: %v %s", err, out)
	}
	config, pem, pin, err := guestTLS(t.TempDir(), netip.MustParseAddr("::1"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, key, hits := nativeGuestFixture(t, config.Certificates[0], "::1")
	file := guestCommandRecipe(t, endpoint, key, pem, pin)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "mcp-stdio", "--guest", file)
	cmd.Dir, cmd.Env = t.TempDir(), nativeProbeEnv(t.TempDir(), "", "")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); cancel(); _ = cmd.Wait() })
	scanner := bufio.NewScanner(output)
	for run := range 2 {
		if _, err := io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"id\":27,\"method\":\"server/discover\"}\n"); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() || !bytes.Contains(scanner.Bytes(), []byte(`"result":`)) {
			t.Fatalf("snapshot round %d failed: %v", run, scanner.Err())
		}
		if run == 0 {
			// Corrupt the same path AFTER readiness was proved by an actual reply.
			// A reload would fail rather than serve the second request.
			if err := os.WriteFile(file, []byte("not the startup invitation"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("guest did not exit cleanly on stdin EOF: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("snapshot request changed endpoint or retried: %d", hits.Load())
	}
}

func guestReplyFixture(t *testing.T, cert tls.Certificate, maxTLS uint16, handler http.Handler) string {
	t.Helper()
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0), Handler: handler,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: maxTLS, Certificates: []tls.Certificate{cert}},
	}
	go func() { _ = srv.Serve(tls.NewListener(listener, srv.TLSConfig)) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "https://" + listener.Addr().String() + "/mcp"
}
