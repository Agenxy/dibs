// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/paths"
)

func TestIdentityRecoveryBridgeProcess(t *testing.T) {
	if nonce := os.Getenv("DIBS_TEST_CACHE_WRITE_NONCE"); nonce != "" {
		rememberNonce(os.Getenv("DIBS_TEST_CACHE_PROJECT"), "worker", nonce)
		return
	}
	if os.Getenv("DIBS_TEST_IDENTITY_RECOVERY_BRIDGE") != "1" {
		return
	}
	if err := runBridge(nil); err != nil {
		t.Fatal(err)
	}
}

type recoveryStdio struct {
	dir       string
	srv       *httptest.Server
	eng       *engine.Engine
	call      func(string, map[string]any) map[string]any
	exchange  func(string, map[string]any) []byte
	drop      atomic.Bool
	refuse    atomic.Bool
	registers atomic.Int64
	thread    string
}

const recoveryFixtureThread = "01a10520-0000-7000-8000-000000000010"

func newRecoveryStdio(t *testing.T) *recoveryStdio {
	return newRecoveryStdioWithPin(t, "")
}

func newRecoveryStdioWithPin(t *testing.T, pin string) *recoveryStdio {
	t.Helper()
	dir := cliIPCProfile(t)
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "recovery-host", box)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(core.NewState("recovery-host", core.DefaultLimits()), led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	f := &recoveryStdio{dir: dir, eng: eng, thread: recoveryFixtureThread}
	server := mcp.New(eng)
	srv := newLocalTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		register := bytes.Contains(b, []byte(`"name":"register"`))
		if f.refuse.Load() {
			if register {
				f.registers.Add(1)
			}
			if bytes.Contains(b, []byte(`"recovery_nonces"`)) {
				// The old-schema RPC refusal is independently measured by the
				// real old-source MCP controls. Here it drives the new bridge.
				_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"register does not take recovery_nonces; nothing was changed"}}`)
				return
			}
		}
		if register && f.drop.Swap(false) {
			server.ServeHTTP(httptest.NewRecorder(), r)
			w.WriteHeader(http.StatusBadGateway) // accepted op, deliberately lost reply
			return
		}
		server.ServeHTTP(w, r)
	}))
	f.srv = srv.Server
	t.Cleanup(func() {
		srv.Close()
		cancel()
		<-done
		if err := led.Close(); err != nil {
			t.Error(err)
		}
	})
	for name, body := range map[string]string{"local.secret": strings.Repeat("a", 64), "dibs.toml": "[wake]\nsockets = false\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	childCtx, stop := context.WithCancel(context.Background())
	cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestIdentityRecoveryBridgeProcess$")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "USERPROFILE=" + dir, "TMPDIR=" + os.TempDir(), "TEMP=" + os.TempDir(), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "DIBS_TEST_IDENTITY_RECOVERY_BRIDGE=1", "DIBS_DIR=" + dir, "DIBS_ADDR=" + strings.TrimPrefix(srv.URL, "http://"), "DIBS_HOST_ID=recovery-host", "DIBS_NOTIFY=off"}
	if pin != "" {
		cmd.Env = append(cmd.Env, "DIBS_AGENT_NONCE="+pin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); stop(); _ = cmd.Wait() })
	reader := bufio.NewReader(out)
	f.exchange = func(method string, params map[string]any) []byte {
		t.Helper()
		request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
		if f.thread != "" {
			request["params"].(map[string]any)["_meta"] = map[string]any{"threadId": f.thread}
		}
		if err := json.NewEncoder(in).Encode(request); err != nil {
			t.Fatal(err)
		}
		read := make(chan []byte, 1)
		go func() { b, _ := reader.ReadBytes('\n'); read <- b }()
		select {
		case b := <-read:
			return b
		case <-time.After(10 * time.Second):
			t.Fatal("actual bridge did not answer before deadline")
			return nil
		}
	}
	f.call = func(name string, args map[string]any) map[string]any {
		t.Helper()
		return recoveryStdioResult(t, f.exchange("tools/call", map[string]any{"name": name, "arguments": args}))
	}
	return f
}

// One real stdio bridge can relay calls from different harness threads. The
// harness supplies the thread metadata; the model supplies only a role name.
// Neither an index setter nor a caller-chosen nonce enters this regression.
func TestRecoveryCacheDoesNotGiveTwoThreadsTheSameCredential(t *testing.T) {
	f := newRecoveryStdio(t)
	project := t.TempDir()
	const firstThread = "01a10520-0000-7000-8000-000000000001"
	const secondThread = "01a10520-0000-7000-8000-000000000002"
	f.thread = firstThread
	a := recoveryStdioOK(t, f.call("register", map[string]any{"name": "worker", "cwd": project}))
	recoveryStdioOK(t, f.call("check_in", map[string]any{"token": a["token"]}))
	f.thread = secondThread
	b := recoveryStdioOK(t, f.call("register", map[string]any{"name": "worker", "cwd": project}))
	if a["agent_id"] == b["agent_id"] || a["token"] == b["token"] {
		t.Fatal("two harness threads received one role credential; the second silently revokes the first")
	}
	f.thread = firstThread
	recoveryStdioOK(t, f.call("check_in", map[string]any{"token": a["token"]}))
	again := recoveryStdioOK(t, f.call("register", map[string]any{"name": "worker", "cwd": project}))
	if again["agent_id"] != a["agent_id"] {
		t.Fatal("the original harness thread did not recover its own identity")
	}
}

func recoveryStdioResult(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var reply struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(b, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Error != nil {
		return map[string]any{"rpc_error": reply.Error}
	}
	if len(reply.Result.Content) != 1 {
		t.Fatal("actual bridge sent no tool content")
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func recoveryStdioOK(t *testing.T, r map[string]any) map[string]any {
	t.Helper()
	id, _ := r["agent_id"].(string)
	token, _ := r["token"].(string)
	if r["ok"] != true && (id == "" || token == "") && r["message"] == nil {
		t.Fatalf("setup/operation failed: %v", r)
	}
	return r
}

func (f *recoveryStdio) direct(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, f.srv.URL, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return recoveryStdioResult(t, raw)
}

func writeLegacyRecoveryCache(t *testing.T, dir string, entries map[string]string) {
	t.Helper()
	writeNamedRecoveryCache(t, dir, nonceStore, entries)
}

func writeNamedRecoveryCache(t *testing.T, dir, name string, entries map[string]string) {
	t.Helper()
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityRecoveryRealStdioNativeCase(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native case spelling requires Darwin F_GETPATH")
	}
	f := newRecoveryStdio(t)
	root := filepath.Join(t.TempDir(), "Supgang")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(root), "SupGang")
	a, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(alias)
	if os.IsNotExist(err) {
		t.Skip("fixture volume is case-sensitive")
	}
	if err != nil || !os.SameFile(a, b) {
		t.Fatalf("setup: case aliases not one object: %v", err)
	}
	first := recoveryStdioOK(t, f.call("register", map[string]any{"name": "worker", "cwd": alias}))
	sender := recoveryStdioOK(t, f.direct(t, "register", map[string]any{"name": "sender", "nonce": "sender-secret"}))
	recoveryStdioOK(t, f.direct(t, "check_in", map[string]any{"token": sender["token"]}))
	mail := recoveryStdioOK(t, f.direct(t, "send", map[string]any{"token": sender["token"], "to": first["agent_id"], "type": "question", "body": "original case mail"}))
	second := recoveryStdioOK(t, f.call("register", map[string]any{"name": "worker", "cwd": root}))
	if first["agent_id"] != second["agent_id"] {
		t.Fatalf("same native directory forked an identity: %v -> %v", first["agent_id"], second["agent_id"])
	}
	read := recoveryStdioOK(t, f.call("read_mail", map[string]any{"token": second["token"], "msg_serial": mail["msg_serial"]}))
	raw, _ := json.Marshal(read)
	if !bytes.Contains(raw, []byte("original case mail")) {
		t.Fatal("original mail stranded")
	}
}

func TestIdentityRecoveryRealStdioSessionAliasConflict(t *testing.T) {
	for _, mode := range []string{"two-valid", "one-valid", "none-valid", "over-limit", "response-loss", "old-daemon-refusal", "one-alias", "same-credential", "explicit", "transport-pin"} {
		t.Run(mode, func(t *testing.T) {
			pin := ""
			if mode == "transport-pin" {
				pin = "new-secret"
			}
			f := newRecoveryStdioWithPin(t, pin)
			root := t.TempDir()
			// A symlink is native same-file evidence on Linux too. The Darwin
			// case-only fixture above separately proves the measured failure.
			alias := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-alias")
			if err := os.Symlink(root, alias); err != nil {
				t.Skipf("fixture cannot create same-file alias: %v", err)
			}
			t.Cleanup(func() { _ = os.Remove(alias) })
			old := recoveryStdioOK(t, f.direct(t, "register", map[string]any{"name": "worker", "nonce": "old-secret", "cwd": root}))
			newer := recoveryStdioOK(t, f.direct(t, "register", map[string]any{"name": "worker", "nonce": "new-secret", "cwd": root}))
			if old["agent_id"] == newer["agent_id"] {
				t.Fatal("setup: no sibling")
			}
			entries := map[string]string{root + "\x00worker": "new-secret", alias + "\x00worker": "old-secret"}
			if mode == "one-alias" {
				delete(entries, root+"\x00worker")
			}
			if mode == "same-credential" {
				entries[root+"\x00worker"] = "old-secret"
			}
			if mode == "one-valid" {
				entries[root+"\x00worker"] = "unknown"
			}
			if mode == "none-valid" {
				entries[root+"\x00worker"] = "unknown-a"
				entries[alias+"\x00worker"] = "unknown-b"
			}
			if mode == "over-limit" {
				for i := 0; i < 17; i++ {
					link := filepath.Join(filepath.Dir(root), fmt.Sprintf("alias-%d", i))
					if err := os.Symlink(root, link); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Remove(link) })
					entries[link+"\x00worker"] = fmt.Sprintf("secret-%d", i)
				}
			}
			// These are aliases retained by THIS harness thread. The old
			// project/name-only namespace has no such ownership evidence and
			// is tested separately below: it must not be injected.
			bound := map[string]string{}
			for key, nonce := range entries {
				dir, name, ok := strings.Cut(key, "\x00")
				if !ok {
					t.Fatal("setup: invalid retained cache key")
				}
				bound[dir+"\x00"+nonceCacheName(name, "codex-thread:"+f.thread)] = nonce
			}
			entries = bound
			writeNamedRecoveryCache(t, f.dir, scopedNonceStore, entries)
			before, err := f.eng.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "response-loss" {
				f.drop.Store(true)
			}
			if mode == "old-daemon-refusal" {
				f.refuse.Store(true)
			}
			args := map[string]any{"name": "worker", "cwd": root}
			if mode == "explicit" {
				args["nonce"] = "new-secret"
			}
			got := f.call("register", args)
			if mode == "one-alias" || mode == "same-credential" || mode == "explicit" || mode == "transport-pin" {
				recoveryStdioOK(t, got)
				want := old["agent_id"]
				if mode == "explicit" || mode == "transport-pin" {
					want = newer["agent_id"]
				}
				if got["agent_id"] != want || got["recovery_nonce_index"] != nil {
					t.Fatal("single credential or explicit/pinned precedence failed")
				}
			}
			if mode == "old-daemon-refusal" {
				if got["rpc_error"] == nil || f.registers.Load() != 1 {
					t.Fatal("old daemon's refusal was hidden or retried")
				}
				after, err := f.eng.Board(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if after["serial"] != before["serial"] {
					t.Fatal("old-daemon refusal changed state")
				}
				b, err := os.ReadFile(filepath.Join(f.dir, scopedNonceStore))
				if err != nil {
					t.Fatal(err)
				}
				var untouched map[string]string
				if err := json.Unmarshal(b, &untouched); err != nil {
					t.Fatal(err)
				}
				if len(untouched) != len(entries) {
					t.Fatal("old-daemon refusal minted or committed a credential")
				}
				return
			}
			if mode == "response-loss" {
				if got["rpc_error"] == nil {
					t.Fatal("setup: accepted response was not lost")
				}
				b, err := os.ReadFile(filepath.Join(f.dir, scopedNonceStore))
				if err != nil {
					t.Fatal(err)
				}
				var untouched map[string]string
				if err := json.Unmarshal(b, &untouched); err != nil {
					t.Fatal(err)
				}
				if len(untouched) != len(entries) {
					t.Fatal("lost reply committed a cache selection")
				}
				got = f.call("register", map[string]any{"name": "worker", "cwd": root})
			}
			switch mode {
			case "two-valid", "one-valid", "response-loss":
				recoveryStdioOK(t, got)
				if got["agent_id"] != old["agent_id"] {
					t.Fatalf("cache migration chose the wrong identity: %v", got)
				}
				if got["recovery_nonce_index"] == nil {
					t.Fatal("real bridge did not ask for credential selection")
				}
			case "none-valid", "over-limit":
				want := "E_RECOVERY_UNPROVEN"
				if mode == "over-limit" {
					want = "E_BAD_ARG"
				}
				if got["code"] != want {
					t.Fatalf("unproven/overflow recovery did not refuse: %v", got)
				}
				after, err := f.eng.Board(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if after["serial"] != before["serial"] {
					t.Fatal("failed recovery created state")
				}
			}
			b, err := os.ReadFile(filepath.Join(f.dir, scopedNonceStore))
			if err != nil {
				t.Fatal(err)
			}
			var retained map[string]string
			if err := json.Unmarshal(b, &retained); err != nil {
				t.Fatal("session cache JSON is unreadable")
			}
			for key, value := range entries {
				if retained[key] != value {
					t.Fatal("migration deleted a legacy credential")
				}
			}
		})
	}
}

func TestLegacyNonceIsClaimedByOnlyTheFirstThread(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit-%t", explicit), func(t *testing.T) { legacyClaimSequence(t, explicit) })
	}
}

func legacyClaimSequence(t *testing.T, explicit bool) {
	t.Helper()
	f := newRecoveryStdio(t)
	project := t.TempDir()
	old := recoveryStdioOK(t, f.direct(t, "register", map[string]any{
		"name": "worker", "nonce": "legacy-source-secret", "cwd": project,
	}))
	key := project + "\x00worker"
	legacy := map[string]string{key: "legacy-source-secret", canonicalNonceKey(project, "worker"): "legacy-source-secret"}
	writeLegacyRecoveryCache(t, f.dir, legacy)
	args := map[string]any{"name": "worker", "cwd": project}
	if explicit {
		args["nonce"] = "legacy-source-secret"
	}
	got := recoveryStdioOK(t, f.call("register", args))
	if got["agent_id"] != old["agent_id"] {
		t.Fatal("upgrade forked the first session instead of migrating its existing credential")
	}
	f.thread = "01a10520-0000-7000-8000-000000000020"
	other := recoveryStdioOK(t, f.call("register", map[string]any{"name": "worker", "cwd": project}))
	if other["agent_id"] == old["agent_id"] {
		t.Fatal("the second thread borrowed the already claimed legacy credential")
	}
	f.thread = recoveryFixtureThread
	recoveryStdioOK(t, f.call("check_in", map[string]any{"token": got["token"]}))
	f.thread = "01a10520-0000-7000-8000-000000000020"
	recoveryStdioOK(t, f.call("check_in", map[string]any{"token": other["token"]}))
	stored := loadRecoveryCache(t, f.dir)
	for key := range legacy {
		if stored[key] != "" {
			t.Fatal("migration left an unscoped copy available to another thread")
		}
	}
	// A pre-upgrade writer may leave the old format again. The durable owner
	// marker must prevent a later new session from claiming it a second time.
	t.Setenv("DIBS_DIR", f.dir)
	writeLegacyRecoveryCache(t, f.dir, legacy) // stale whole-file write by an old bridge
	f.thread = "01a10520-0000-7000-8000-000000000030"
	late := recoveryStdioOK(t, f.call("register", map[string]any{"name": "worker", "cwd": project}))
	if late["agent_id"] == old["agent_id"] {
		t.Fatal("a late legacy-format write let a second session claim the same seat")
	}
}

func loadRecoveryCache(t *testing.T, dir string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, nonceStore))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]string
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestBusyLegacyMigrationRefusesBeforeForwardingRegistration(t *testing.T) {
	f := newRecoveryStdio(t)
	t.Setenv("DIBS_DIR", f.dir)
	project := t.TempDir()
	old := recoveryStdioOK(t, f.direct(t, "register", map[string]any{
		"name": "worker", "nonce": "legacy-source-secret", "cwd": project,
	}))
	writeLegacyRecoveryCache(t, f.dir, map[string]string{project + "\x00worker": "legacy-source-secret"})
	lock, err := os.OpenFile(noncePath()+".scope.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal("setup: kernel lock file", err)
	}
	defer func() { _ = lock.Close() }()
	err = paths.LockExclusive(lock, false)
	if err != nil {
		t.Fatal("setup: real kernel lock", err)
	}
	guard := func() { paths.Unlock(lock) }
	held := true
	defer func() {
		if held {
			guard()
		}
	}()
	before, err := f.eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := f.call("register", map[string]any{"name": "worker", "cwd": project})
	if !strings.Contains(fmt.Sprint(r["rpc_error"]), "E_NONCE_CACHE") {
		t.Fatal("busy migration was hidden, minted a fresh identity or reached the daemon")
	}
	after, err := f.eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before["serial"] != after["serial"] {
		t.Fatal("refused migration forwarded a registration")
	}
	recoveryStdioOK(t, f.direct(t, "check_in", map[string]any{"token": old["token"]}))
	guard()
	held = false
	resumed := recoveryStdioOK(t, f.call("register", map[string]any{"name": "worker", "cwd": project}))
	if resumed["agent_id"] != old["agent_id"] {
		t.Fatal("same-session retry did not resume the original seat")
	}
}

func TestConcurrentRecoveryCacheWritesRetainEveryCredential(t *testing.T) {
	dir, project := t.TempDir(), t.TempDir()
	key := project + "\x00worker"
	writeLegacyRecoveryCache(t, dir, map[string]string{key: "legacy-secret"})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIdentityRecoveryBridgeProcess$")
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "USERPROFILE=" + dir, "TMPDIR=" + os.TempDir(), "TEMP=" + os.TempDir(), "SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "DIBS_DIR=" + dir, "DIBS_TEST_CACHE_PROJECT=" + project, "DIBS_TEST_CACHE_WRITE_NONCE=" + fmt.Sprintf("held-secret-%d", i)}
			if err := cmd.Run(); err != nil {
				t.Error("setup: credential writer failed:", err)
			}
		}()
	}
	wg.Wait()
	b, err := os.ReadFile(filepath.Join(dir, "harness-nonces.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]string
	if err := json.Unmarshal(b, &stored); err != nil {
		t.Fatal("old bridge cannot read cache after concurrent writers")
	}
	if stored[key] != "legacy-secret" {
		t.Fatal("concurrent writers replaced the retained legacy credential")
	}
	for i := range 8 {
		found := false
		for _, nonce := range stored {
			found = found || nonce == fmt.Sprintf("held-secret-%d", i)
		}
		if !found {
			t.Fatal("concurrent writers lost a held credential")
		}
	}
}

// A malformed cache is evidence of lost state, never permission to mint over it.
func TestCorruptCredentialCacheRefusesBeforeRegistration(t *testing.T) {
	for _, filename := range []string{nonceStore, scopedNonceStore} {
		t.Run(filename, func(t *testing.T) {
			f := newRecoveryStdio(t)
			path := filepath.Join(f.dir, filename)
			raw := []byte("{interrupted-cache")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal("setup: damaged cache", err)
			}
			before, err := f.eng.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			r := f.call("register", map[string]any{"name": "worker", "cwd": t.TempDir()})
			if !strings.Contains(fmt.Sprint(r["rpc_error"]), "E_NONCE_CACHE") {
				t.Fatal("corrupt cache did not refuse registration")
			}
			after, err := f.eng.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if before["serial"] != after["serial"] {
				t.Fatal("corrupt cache forwarded registration")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(raw) {
				t.Fatal("corrupt cache was overwritten")
			}
		})
	}
}
