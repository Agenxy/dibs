package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agenxy/dibs/internal/paths"
)

// Enter through the actual command and its pinned TLS transport, not a nonce
// helper. A good request first proves the startup fixture is usable; later
// requests prove that a refused register did not kill the MCP process.
func TestGuestNonceStoreRefusalKeepsActualBridgeAlive(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dibs")
	if out, err := exec.Command("go", "build", "-o", bin, "../dibs").CombinedOutput(); err != nil {
		t.Fatalf("build actual guest bridge: %v %s", err, out)
	}
	config, pem, pin, err := guestTLS(t.TempDir(), netip.MustParseAddr("::1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cause := range []string{"corrupt", "unsafe", "contended"} {
		t.Run(cause, func(t *testing.T) {
			var hits, registers atomic.Int64
			key := "dibs_inv_" + strings.Repeat("a", 64)
			callerNonce := strings.Repeat("b", 64)
			endpoint := guestReplyFixture(t, config.Certificates[0], tls.VersionTLS13,
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					var req struct {
						ID     json.RawMessage `json:"id"`
						Params struct {
							Name string `json:"name"`
							Args struct {
								Nonce string `json:"nonce"`
							} `json:"arguments"`
						} `json:"params"`
					}
					if json.NewDecoder(r.Body).Decode(&req) != nil || r.Header.Get("Authorization") != "Bearer "+key {
						t.Error("actual bridge fixture received invalid request/authentication")
						return
					}
					if req.Params.Name == "register" {
						registers.Add(1)
						if req.Params.Args.Nonce != callerNonce || string(req.ID) != "4" {
							t.Error("failed auto-nonce registration reached the wire or caller nonce changed")
						}
					}
					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(map[string]any{
						"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{},
					}); err != nil {
						t.Error(err)
					}
				}))
			file := guestCommandRecipe(t, endpoint, key, pem, pin)
			store := prepareRefusedGuestStore(t, file, endpoint, pin, cause)
			before := guestRefusalFiles(t, filepath.Dir(file))
			input := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
				`{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"register","arguments":{"name":"native-guest"}}}` + "\n" +
				`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"register","arguments":{"name":"native-guest"}}}` + "\n" +
				`{"jsonrpc":"2.0","id":3,"method":"tools/list"}` + "\n" +
				`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"register","arguments":{"name":"native-guest","nonce":"` + callerNonce + `"}}}`
			out := guestCommandOutput(t, bin, file, []byte(input))
			assertGuestNonceRefusal(t, out, cause)
			if hits.Load() != 3 || registers.Load() != 1 {
				t.Fatal("failed registration transmitted/retried or later valid requests did not run")
			}
			for _, secret := range []string{file, store, key, callerNonce, "corrupt-recovery-must-not-leak"} {
				if bytes.Contains(out, []byte(secret)) {
					t.Fatal("refusal leaked a private path or credential into the transcript")
				}
			}
			if !bytes.Equal(before, guestRefusalFiles(t, filepath.Dir(file))) {
				t.Fatal("refusal left a new nonce/temp/lock file or replaced retained state")
			}
		})
	}
}

func prepareRefusedGuestStore(t *testing.T, file, endpoint, pin, cause string) string {
	t.Helper()
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var recipe map[string]any
	if err := json.Unmarshal(body, &recipe); err != nil {
		t.Fatal(err)
	}
	delete(recipe, "recovery_nonce")
	body, err = json.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(endpoint + "\x00" + pin + "\x00native-guest"))
	store := filepath.Join(filepath.Dir(file), "guest-recovery-"+hex.EncodeToString(sum[:])+".nonce")
	lock, err := os.OpenFile(store+".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { paths.Unlock(lock); _ = lock.Close() })
	if cause == "contended" {
		if err := paths.LockExclusive(lock, false); err != nil {
			t.Fatal("lock contention setup failed")
		}
		return store
	}
	if err := os.WriteFile(store, []byte("corrupt-recovery-must-not-leak"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cause == "unsafe" {
		if err := os.Chmod(store, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func guestRefusalFiles(t *testing.T, dir string) []byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot bytes.Buffer
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		snapshot.WriteString(entry.Name())
		snapshot.WriteByte(0)
		snapshot.Write(body)
		snapshot.WriteByte(0)
	}
	return snapshot.Bytes()
}

func assertGuestNonceRefusal(t *testing.T, out []byte, cause string) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	if len(lines) != 4 {
		t.Fatal("refusal killed later requests or synthesized a notification response")
	}
	for i, id := range []string{"1", "9007199254740993", "3", "4"} {
		var reply struct {
			Version string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   *struct {
				Data struct {
					Hint string `json:"hint"`
				} `json:"data"`
			} `json:"error"`
		}
		if json.Unmarshal(lines[i], &reply) != nil || reply.Version != "2.0" || string(reply.ID) != id {
			t.Fatal("guest refusal changed an exact request ID or damaged the JSON-RPC envelope")
		}
		if i != 1 {
			if reply.Error != nil || len(reply.Result) == 0 {
				t.Fatal("setup/later valid request failed through actual TLS bridge")
			}
			continue
		}
		want := "restore the private store, or ask the issuer for an export recipe carrying the recovery nonce"
		if cause == "contended" {
			want = "another bridge for this guest is registering; retry the call in a few seconds"
		}
		if reply.Error == nil || reply.Error.Data.Hint != want || len(reply.Result) != 0 {
			t.Fatal("nonce-store refusal lacks the cause-specific corrective hint or fakes success")
		}
	}
}
