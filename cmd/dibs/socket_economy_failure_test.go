package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A real child and stdio request reach the real HTTP route, where this fixture
// holds board's reply. No ready/diagnostic setter stands in for that path.
func TestSocketEconomyTimeoutDiagnosticsHelper(t *testing.T) {
	if os.Getenv("DIBS_TEST_ECONOMY_TIMEOUT_CONTROL") != "1" {
		return
	}
	release := make(chan struct{})
	f := newEconomyFixtureWith(t, "71f97290-0001-4000-8000-111111111111", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error("setup: HTTP body:", err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			var req struct {
				Params struct{ Name string } `json:"params"`
			}
			if json.Unmarshal(b, &req) != nil {
				t.Error("setup: actual stdio request was not JSON")
				return
			}
			if req.Params.Name == "board" {
				fmt.Fprintln(os.Stderr, "DIAGNOSTIC_CONTROL_HOLDS_BOARD_REPLY")
				<-release
			}
			next.ServeHTTP(w, r)
		})
	})
	t.Cleanup(func() { close(release) }) // unblock before server Cleanup joins requests
	f.stdio(t, "board", map[string]any{"token": f.worker})
	t.Fatal("timeout control unexpectedly received a reply")
}

func TestSocketEconomyTimeoutReportsActualBridgeDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSocketEconomyTimeoutDiagnosticsHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "DIBS_TEST_ECONOMY_TIMEOUT_CONTROL=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil || err == nil {
		t.Fatalf("setup: real blocked-HTTP timeout control did not fail normally: %v %v\n%s", err, ctx.Err(), out)
	}
	for _, required := range []string{
		"DIAGNOSTIC_CONTROL_HOLDS_BOARD_REPLY",
		"board did not reply within 5s", "stdout read=pending: ReadBytes has not returned",
		"child exit=running or exit not yet reaped", "stderr tail (max 65536 bytes)",
		"bridge goroutines:", "runBridge(", "net/http.(*persistConn).roundTrip",
	} {
		if !strings.Contains(string(out), required) {
			t.Errorf("real timeout lost diagnostic %q:\n%s", required, out)
		}
	}
	if strings.Contains(string(out), "setup: actual stdio request was not JSON") || strings.Contains(string(out), "setup: HTTP body:") {
		t.Fatalf("control setup failed before its intended timeout:\n%s", out)
	}
}
