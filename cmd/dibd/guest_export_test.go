package main

import (
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/invites"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
)

// The actual CLI dispatches --out through real MCP issuance. This is private
// export acceptance, not published-artifact, harness or external WAN acceptance.
func TestGuestInviteExportThroughActualCLI(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", bin, "../dibs").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, out)
	}
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "guest-export", box)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(core.NewState("guest-export", core.DefaultLimits()), led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; _ = led.Close() })
	issuer, err := eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "issuer", Nonce: "fixture-retained-issuer-nonce"})
	if err != nil || issuer["token"] == nil {
		t.Fatal("issuer registration setup failed")
	}
	_, ca, pin, err := guestTLS(t.TempDir(), netip.MustParseAddr("::1"))
	if err != nil {
		t.Fatal(err)
	}
	store := invites.Store{Dir: dir}
	service := &invites.Service{
		Store: store, Engine: eng, RecoveryNonce: box.GuestRecoveryNonce,
		Policy: boardconfig.InvitesConfig{MaxTTLS: 3600}, Endpoint: invites.NewPublicEndpoint(invites.EndpointInfo{
			URL: "https://[::1]:4778", Mode: "direct-ip", CAPEM: ca, CASPKIPin: pin,
		}),
	}
	api := mcp.New(eng)
	api.SetInvites(service)
	// Fixture replacement stands for a restarted listener. Subprocess exit is
	// not a Go memory fence, so synchronize replacement with both HTTP readers.
	var endpointMu sync.RWMutex
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpointMu.RLock()
		defer endpointMu.RUnlock()
		if r.Header.Get("X-Dibs-Local") != strings.Repeat("a", 64) {
			t.Error("private CLI credential missing")
		}
		hits.Add(1)
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	cliDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cliDir, "local.secret"), []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) ([]byte, error) {
		t.Helper()
		ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		cmd := exec.CommandContext(ctx, bin, append([]string{"invite"}, args...)...)
		cmd.Env = append(nativeProbeEnv(cliDir, "", ""), "DIBS_DIR="+cliDir, "DIBS_ADDR="+server.URL,
			"DIBS_TOKEN="+issuer["token"].(string))
		return cmd.CombinedOutput()
	}
	private := t.TempDir()
	if err := os.Chmod(private, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(private, "invitation.json")
	out, err := run("issuer-cloud", "--out", file)
	if err != nil {
		t.Fatalf("actual CLI private export failed: %v %s", err, out)
	}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("actual CLI did not publish the private recipe")
	}
	var recipe struct {
		Version  int       `json:"schema_version"`
		Name     string    `json:"name"`
		Endpoint string    `json:"endpoint"`
		Key      string    `json:"invitation_key"`
		Nonce    string    `json:"recovery_nonce"`
		Expires  time.Time `json:"expires_at"`
		CA       string    `json:"ca_pem"`
		Pin      string    `json:"ca_spki_sha256"`
		Release  any       `json:"bridge_release"`
	}
	if err := json.Unmarshal(body, &recipe); err != nil {
		t.Fatal(err)
	}
	expected, err := box.GuestRecoveryNonce("issuer-cloud")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.List()
	if err != nil || len(entries) != 1 {
		t.Fatal("issuance setup did not create exactly one entry")
	}
	if recipe.Version != 1 || recipe.Name != "issuer-cloud" || recipe.Endpoint != "https://[::1]:4778/mcp" ||
		recipe.Nonce != expected || recipe.Key == "" || !recipe.Expires.Equal(entries[0].Expires) || recipe.CA != ca || recipe.Pin != pin || recipe.Release != nil {
		t.Fatal("export lost issuer identity/expiry/trust or invented release metadata")
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("export is not private")
	}
	if !bytes.Contains(out, []byte("INCOMPLETE")) || bytes.Contains(out, []byte(recipe.Key)) || bytes.Contains(out, []byte(recipe.Nonce)) ||
		bytes.Contains(out, []byte("mcp-stdio")) {
		t.Fatal("export output leaked credentials or offered runnable provisioning")
	}
	// --out alone must not turn an omitted --ttl into the hard-coded 7d;
	// this board's 1h ceiling also proves the parser retained issuer policy.
	if time.Until(recipe.Expires) > time.Hour {
		t.Fatal("export flag overrode board TTL default")
	}
	before := hits.Load()
	unsafe := t.TempDir()
	if err := os.Chmod(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(private, "symlink.json")
	if err := os.Symlink(file, symlink); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"issuer-other", "--out", file},
		{"issuer-other", "--out", "relative.json"},
		{"issuer-other", "--out", symlink},
		{"issuer-other", "--out", filepath.Join(unsafe, "recipe.json")},
		{"issuer-other", "--out", ""},
		{"list", "--out", file},
		{"revoke", "issuer-cloud", "--out", file},
	} {
		if _, err := run(args...); err == nil {
			t.Fatal("unsafe or non-mint export accepted")
		}
	}
	if hits.Load() != before {
		t.Fatal("invalid output path minted or changed an invitation")
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(after, body) {
		t.Fatal("existing private recipe was overwritten")
	}
	// Reissue through the issuer CLI, rotate both endpoint and constrained CA,
	// and register from a fresh bridge HOME each time through real TLS/publicGate.
	// No recipe nonce is manually supplied by the fixture or by the caller.
	request := []byte(`{"jsonrpc":"2.0","id":91,"method":"tools/call","params":{"name":"register","arguments":{"name":"issuer-cloud","kind":"persistent","description":"export recovery fixture"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`)
	var firstID, firstPin, firstEndpoint, firstKey string
	for cycle := range 2 {
		if output, err := run("revoke", "issuer-cloud"); err != nil {
			t.Fatalf("revoke setup failed: %v %s", err, output)
		}
		tlsConfig, rotatedCA, rotatedPin, err := guestTLS(t.TempDir(), netip.MustParseAddr("::1"))
		if err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Fatal(err)
		}
		origin := "https://" + listener.Addr().String()
		endpointMu.Lock()
		service.Endpoint = invites.NewPublicEndpoint(invites.EndpointInfo{
			URL: origin, Mode: "direct-ip", CAPEM: rotatedCA, CASPKIPin: rotatedPin,
		})
		endpointMu.Unlock()
		gate := &publicGate{store: store, origin: origin, service: service, rates: map[string]*inviteBucket{}}
		var httpHits atomic.Int64
		next := gate.wrap(mcp.New(eng))
		public := &http.Server{
			ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0),
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				endpointMu.RLock()
				defer endpointMu.RUnlock()
				httpHits.Add(1)
				next.ServeHTTP(w, r)
			}),
		}
		go func() { _ = public.Serve(tls.NewListener(listener, tlsConfig)) }()
		t.Cleanup(func() { _ = public.Close() })
		freshDir := t.TempDir()
		if err := os.Chmod(freshDir, 0o700); err != nil {
			t.Fatal(err)
		}
		freshFile := filepath.Join(freshDir, "invitation.json")
		if output, err := run("issuer-cloud", "--out", freshFile); err != nil {
			t.Fatalf("reissue export failed: %v %s", err, output)
		}
		freshBody, err := os.ReadFile(freshFile)
		if err != nil {
			t.Fatal(err)
		}
		var fresh map[string]any
		if err := json.Unmarshal(freshBody, &fresh); err != nil {
			t.Fatal(err)
		}
		if fresh["recovery_nonce"] != expected || fresh["endpoint"] != origin+"/mcp" || fresh["ca_spki_sha256"] != rotatedPin {
			t.Fatal("reissue export changed recovery identity or retained old endpoint/trust")
		}
		reply := guestCommandOutput(t, bin, freshFile, request)
		var rpc struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(reply, &rpc); err != nil || rpc.Result.IsError || len(rpc.Result.Content) == 0 {
			t.Fatal("fresh bridge was refused through the reissued TLS/publicGate")
		}
		var registration struct {
			ID string `json:"agent_id"`
		}
		if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &registration); err != nil || registration.ID == "" || httpHits.Load() != 1 {
			t.Fatal("fresh bridge did not bind exactly once to a real agent")
		}
		if cycle == 0 {
			firstID, firstPin, firstEndpoint, firstKey = registration.ID, rotatedPin, origin, fresh["invitation_key"].(string)
		} else if registration.ID != firstID || rotatedPin == firstPin || origin == firstEndpoint || fresh["invitation_key"] == firstKey {
			t.Fatal("rotated endpoint/CA/bearer setup failed or fresh bridge created a sibling mailbox")
		}
	}
}
