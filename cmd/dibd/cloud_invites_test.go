package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/adminpw"
	"github.com/agenxy/dibs/internal/blobstore"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/invites"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/transfer"
)

type cloudFixture struct {
	public, private *httptest.Server
	dir, key        string
	t               *testing.T
}

func TestCloudInvitationExpiryAndLegacyHandshakeUseTheSameGate(t *testing.T) {
	f := newCloudFixture(t)
	headers := map[string]string{"Authorization": "Bearer " + f.key, "MCP-Protocol-Version": "2025-11-25"}
	code, out := f.post(f.public, "/mcp", headers, map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"method": "initialize", "params": map[string]any{
			"protocolVersion": "2025-11-25",
			"clientInfo":      map[string]any{"name": "cloud-legacy", "version": "1"}, "capabilities": map[string]any{},
		},
	})
	if code != 200 || out["error"] != nil {
		t.Fatalf("legacy initialize: %d %v", code, out)
	}
	code, out = f.post(f.public, "/mcp", headers, map[string]any{
		"jsonrpc": "2.0", "id": 2,
		"method": "tools/call", "params": map[string]any{"name": "register", "arguments": map[string]any{
			"name": "cloud-worker", "nonce": "retained-legacy-cloud", "kind": "persistent",
		}},
	})
	if code != 200 || out["error"] != nil {
		t.Fatalf("legacy register: %d %v", code, out)
	}
	r, _ := out["result"].(map[string]any)
	if r["isError"] == true {
		t.Fatalf("legacy registration refused: %v", r)
	}
	// A genuinely expired verifier, exercised through the HTTPS listener rather
	// than a store-only assertion. No clock fake is passed to the gate.
	expired, err := (invites.Store{Dir: f.dir}).Mint("expired-worker", time.Second, time.Now().Add(-2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	headers["Authorization"] = "Bearer " + expired
	code, out = f.post(f.public, "/mcp", headers, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "ping"})
	if code != http.StatusUnauthorized || out["code"] != "E_INVITE_AUTH" {
		t.Fatalf("expired invite passed HTTPS door: %d %v", code, out)
	}
}

// Enter at the production flags, listener, proxy and credential gate. The
// client trusts the fixture's CA: no InsecureSkipVerify and no forged context.
func newCloudFixture(t *testing.T) *cloudFixture {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "cloud-test", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("cloud-test", core.DefaultLimits())
	if _, err = led.Replay(st); err != nil {
		t.Fatal(err)
	}
	bs, err := blobstore.New(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, led, nil)
	eng.SetHostID("real-hub")
	eng.SetBlobs(bs)
	eng.SetPrivilegedNames([]string{"local-staff"})
	ctx, stop := context.WithCancel(context.Background())
	go eng.Run(ctx)
	t.Cleanup(func() { stop(); _ = led.Close() })
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reserved.Addr().String()
	if err = reserved.Close(); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewUnstartedServer(nil)
	publicURL := "https://" + proxy.Listener.Addr().String()
	fs := cloudFlagSet()
	opts, _ := registerDaemonFlags(fs)
	if err = fs.Parse([]string{"--public-url", publicURL, "--public-addr", addr}); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolvePublic(opts)
	if err != nil {
		t.Fatal(err)
	}
	files := transfer.New(ctx, eng, bs, dir)
	fail, closePublic, err := startPublic(ctx, cfg, dir, eng, "board-secret", stop, files)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closePublic()
		select {
		case err := <-fail:
			t.Error(err)
		default:
		}
	})
	target, err := url.Parse("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	proxy.Config.Handler = httputil.NewSingleHostReverseProxy(target)
	proxy.StartTLS()
	t.Cleanup(proxy.Close)
	hash, err := adminpw.Hash("operator-proof")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "admin.hash"), []byte(hash), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	privateMCP := mcp.New(eng)
	privateMCP.SetTaskKey("board-secret")
	service := &invites.Service{Engine: eng, Store: invites.Store{Dir: dir}, URL: publicURL}
	privateMCP.SetInvites(service)
	mux.Handle("/mcp", privateMCP)
	registerInvitationAPI(mux, service)
	registerAdminAPI(mux, eng)
	gate := newAuthGate("board-secret", filepath.Join(dir, "admin.hash"), "127.0.0.1:4777")
	private := httptest.NewServer(gate.wrap(mux))
	t.Cleanup(private.Close)
	f := &cloudFixture{public: proxy, private: private, dir: dir, t: t}
	code, minted := f.admin("mint", "cloud-worker")
	if code != 200 {
		t.Fatalf("setup: invitation issuance refused: %d %v", code, minted)
	}
	f.key, _ = minted["key"].(string)
	if !strings.HasPrefix(f.key, invites.Prefix) {
		t.Fatal("setup: no invitation issued")
	}
	return f
}

func (f *cloudFixture) post(s *httptest.Server, path string, headers map[string]string, payload any) (int, map[string]any) {
	f.t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	r, err := http.NewRequest(http.MethodPost, s.URL+path, bytes.NewReader(b))
	if err != nil {
		f.t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	resp, err := s.Client().Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err = io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out) // non-JSON private auth refusals are expected
	return resp.StatusCode, out
}

func (f *cloudFixture) admin(action, name string) (int, map[string]any) {
	return f.post(f.private, "/api/admin/invites", map[string]string{"X-Dibs-Local": "board-secret", "X-Dibs-Admin": "operator-proof"}, map[string]any{"action": action, "name": name, "ttl_s": 3600})
}

func (f *cloudFixture) rpc(public bool, method string, params any) map[string]any {
	f.t.Helper()
	s, key := f.private, "board-secret"
	if public {
		s, key = f.public, f.key
	}
	code, out := f.post(s, "/mcp", map[string]string{"Authorization": "Bearer " + key, "MCP-Protocol-Version": "2026-07-28"}, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if code != 200 {
		f.t.Fatalf("RPC %s failed at HTTP door: %d %v", method, code, out)
	}
	return out
}

func (f *cloudFixture) tool(public bool, name string, args map[string]any) map[string]any {
	return f.toolMeta(public, name, args, nil)
}

func (f *cloudFixture) toolMeta(public bool, name string, args, meta map[string]any) map[string]any {
	f.t.Helper()
	out := f.rpc(public, "tools/call", map[string]any{"name": name, "arguments": args, "_meta": meta})
	if e, ok := out["error"].(map[string]any); ok {
		return e
	}
	r, ok := out["result"].(map[string]any)
	if !ok {
		f.t.Fatalf("%s: no result", name)
	}
	if r["resultType"] == "task" {
		return r
	}
	contents, ok := r["content"].([]any)
	if !ok || len(contents) == 0 {
		f.t.Fatalf("%s: no content", name)
	}
	text, _ := contents[0].(map[string]any)["text"].(string)
	var result map[string]any
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		f.t.Fatal(err)
	}
	return result
}

func cloudRegistered(f *cloudFixture) string {
	f.t.Helper()
	r := f.toolMeta(true, "register", map[string]any{"name": "cloud-worker", "kind": "persistent", "nonce": "cloud-kept-nonce", "cwd": "/workspace/repo", "harness": "codex"}, map[string]any{"com.dibs/host": "real-hub", "threadId": "not-a-hub-session", "com.dibs/repo": map[string]any{"root": "/workspace/repo", "dir": "portable-repo-id", "remote": "https://example.com/repo.git"}})
	token, _ := r["token"].(string)
	if token == "" || r["agent_id"] != "cloud-worker" {
		f.t.Fatalf("registration: %v", r)
	}
	r = f.tool(true, "check_in", map[string]any{"token": token, "detail": true})
	b, _ := json.Marshal(r)
	if !bytes.Contains(b, []byte(`"host_id":"invite:cloud-worker"`)) || bytes.Contains(b, []byte("not-a-hub-session")) {
		f.t.Fatalf("wrong identity/session: %s", b)
	}
	if !bytes.Contains(b, []byte("portable-repo-id")) {
		f.t.Fatal("cloud portable repository identity was discarded")
	}
	return token
}

func TestCloudInvitationRegistersAndCoordinatesOverTrustedHTTPS(t *testing.T) {
	f := newCloudFixture(t)
	token := cloudRegistered(f)
	for name, args := range map[string]map[string]any{
		"declare": {"token": token, "text": "cloud work", "dirs": []string{"/workspace/repo"}},
		"claim":   {"token": token, "path": "/workspace/repo/file", "mode": "exclusive"},
		"inbox":   {"token": token},
	} {
		if r := f.tool(true, name, args); r["code"] != nil {
			t.Fatalf("%s: %v", name, r)
		}
	}
	local := f.tool(false, "register", map[string]any{"name": "local-worker", "nonce": "local-kept-nonce"})
	localToken, _ := local["token"].(string)
	if localToken == "" {
		t.Fatal("setup: local registration failed")
	}
	if r := f.tool(false, "check_in", map[string]any{"token": localToken}); r["code"] != nil {
		t.Fatal(r)
	}
	sent := f.tool(false, "send", map[string]any{"token": localToken, "to": "cloud-worker", "type": "question", "body": "answer from the cloud"})
	if !strings.Contains(sent["note"].(string), "pull-only by design") {
		t.Fatal("cloud delivery note did not explain pull-only access")
	}
	serial := sent["msg_serial"]
	if r := f.tool(true, "read_mail", map[string]any{"token": token, "msg_serial": serial}); r["code"] != nil {
		t.Fatal(r)
	}
	if r := f.tool(true, "respond", map[string]any{"token": token, "msg_serial": serial, "disposition": "answer", "body": "cloud answer"}); r["state"] != "answered" {
		t.Fatal(r)
	}
	if r := f.tool(true, "register", map[string]any{"name": "cloud-worker", "kind": "persistent", "nonce": "cloud-kept-nonce"}); r["agent_id"] != "cloud-worker" {
		t.Fatal("same nonce did not recover bound identity")
	}
	resumed := f.tool(true, "resume", map[string]any{"nonce": "cloud-kept-nonce", "resume_id": "cloud-activation-1"})
	if resumed["agent_id"] != "cloud-worker" || resumed["token"] == "" {
		t.Fatalf("own persistent resume failed: %v", resumed)
	}
	if code, _ := f.admin("revoke", "cloud-worker"); code != 200 {
		t.Fatal("revoke failed")
	}
	code, _ := f.post(f.public, "/mcp", map[string]string{"Authorization": "Bearer " + f.key}, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if code != 401 {
		t.Fatalf("revoked on next request: %d", code)
	}
	b, err := os.ReadFile(filepath.Join(f.dir, "invites.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(f.key)) || !bytes.Contains(b, []byte(`"agent_id": "cloud-worker"`)) {
		t.Fatal("raw key persisted or binding lost")
	}
}

func TestCloudInvitationScopesTasksAndRateLimit(t *testing.T) {
	f := newCloudFixture(t)
	token := cloudRegistered(f)
	first := f.tool(false, "register", map[string]any{"name": "local-first"})["token"]
	second := f.tool(false, "register", map[string]any{"name": "local-second"})["token"]
	if first == nil || second == nil {
		t.Fatal("setup: private participants not registered")
	}
	caps := map[string]any{"io.modelcontextprotocol/clientCapabilities": map[string]any{"extensions": map[string]any{"io.modelcontextprotocol/tasks": map[string]any{}}}}
	foreign := f.toolMeta(false, "send", map[string]any{"token": first, "to": "local-second", "type": "request", "body": "private task contents", "track": true}, caps)
	if foreign["resultType"] != "task" {
		t.Fatalf("setup: no foreign task handle: %v", foreign)
	}
	own := f.toolMeta(true, "send", map[string]any{"token": token, "to": "local-first", "type": "request", "body": "own cloud task", "track": true}, caps)
	if own["resultType"] != "task" {
		t.Fatalf("setup: no own task handle: %v", own)
	}
	meta := map[string]any{"com.dibs/token": token, "io.modelcontextprotocol/clientCapabilities": caps["io.modelcontextprotocol/clientCapabilities"]}
	for _, method := range []string{"tasks/get", "tasks/update", "tasks/cancel"} {
		if out := f.rpc(true, method, map[string]any{"taskId": foreign["taskId"], "_meta": meta}); out["error"] == nil {
			t.Fatalf("%s read another identity's task", method)
		}
		if out := f.rpc(true, method, map[string]any{"taskId": own["taskId"], "_meta": meta}); out["error"] != nil {
			t.Fatalf("%s refused own task: %v", method, out)
		}
	}
	if out := f.rpc(true, "tasks/get", map[string]any{"taskId": own["taskId"], "_meta": caps}); out["error"] == nil {
		t.Fatal("task handle alone bypassed invite identity")
	}
	code, secondInvite := f.admin("mint", "cloud-second")
	if code != 200 {
		t.Fatal("setup: independent invite failed")
	}
	limited := false
	for i := 0; i < 60; i++ {
		code, _ := f.post(f.public, "/mcp", map[string]string{"Authorization": "Bearer " + f.key}, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
		if code == 429 {
			limited = true
			break
		}
		if code != 200 {
			t.Fatalf("unexpected rate response: %d", code)
		}
	}
	if !limited {
		t.Fatal("public invitation was not rate limited")
	}
	code, _ = f.post(f.public, "/mcp", map[string]string{"Authorization": "Bearer " + secondInvite["key"].(string)}, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if code != 200 {
		t.Fatal("one invite's throttle rate-limited another")
	}
}

func TestCloudInvitationLocalIssuerNeedsNoHumanAndCannotEscalate(t *testing.T) {
	f := newCloudFixture(t)
	parent := f.tool(false, "register", map[string]any{"name": "local-lead", "kind": "persistent", "nonce": "local-lead-nonce"})
	token, _ := parent["token"].(string)
	if token == "" {
		t.Fatal("setup: issuer not registered")
	}
	var children []map[string]any
	for i := 0; i < 4; i++ {
		c := f.tool(false, "invite", map[string]any{"token": token})
		if c["issued_by"] != "local-lead" || c["key"] == nil || c["config"] == nil {
			t.Fatalf("autonomous issuance failed: %v", c)
		}
		if !strings.HasPrefix(c["name"].(string), "local-lead-cloud-") {
			t.Fatal("automatic name escaped issuer prefix")
		}
		children = append(children, c)
	}
	for _, args := range []map[string]any{{"token": token}, {"token": token, "name": "unrelated"}, {"token": token, "name": "local-lead-long", "ttl_s": 8 * 24 * 3600}} {
		if r := f.tool(false, "invite", args); r["code"] == nil || r["hint"] == "" {
			t.Fatalf("issuer cap/name/TTL boundary bypassed: %v", r)
		}
	}
	other := f.tool(false, "register", map[string]any{"name": "other-lead"})["token"]
	if other == nil {
		t.Fatal("setup: other issuer missing")
	}
	if r := f.tool(false, "invite", map[string]any{"token": other, "action": "revoke", "name": children[0]["name"]}); r["code"] == nil {
		t.Fatal("other issuer revoked a child")
	}
	if r := f.tool(false, "invite", map[string]any{"token": token, "action": "list"}); len(r["invites"].([]any)) != 4 {
		t.Fatal("issuer did not see its own four invites")
	}
	if r := f.tool(false, "invite", map[string]any{"token": token, "action": "revoke", "issued_by": "local-lead"}); r["code"] != nil {
		t.Fatal(r)
	}
	if _, err := (invites.Store{Dir: f.dir}).Authenticate(children[0]["key"].(string), time.Now()); err == nil {
		t.Fatal("bulk revoke did not invalidate a child's next request")
	}
	if r := f.tool(false, "register", map[string]any{"name": "local-lead-helper"}); r["token"] == nil {
		t.Fatalf("setup: narrower issuer namespace missing: %v", r)
	}
	if r := f.tool(false, "invite", map[string]any{"token": token, "name": "local-lead-helper-child"}); r["code"] != "E_INVITE_SCOPE" {
		t.Fatalf("broad issuer squatted on another namespace: %v", r)
	}
	child := f.tool(false, "invite", map[string]any{"token": token})
	f.key = child["key"].(string)
	childReg := f.tool(true, "register", map[string]any{"name": child["name"], "kind": "persistent", "nonce": "child-kept-nonce"})
	childToken, _ := childReg["token"].(string)
	if childToken == "" {
		t.Fatalf("setup: child registration: %v", childReg)
	}
	if code, _ := f.post(f.private, "/api/admin/role", map[string]string{"X-Dibs-Local": "board-secret", "X-Dibs-Admin": "operator-proof"}, map[string]any{"agent": child["name"], "role": "coordinator"}); code != 200 {
		t.Fatal("setup: granted child staff role failed")
	}
	for _, public := range []bool{true, false} {
		if r := f.tool(public, "invite", map[string]any{"token": childToken, "name": "child-grandchild"}); r["code"] != "E_INVITE_SCOPE" {
			t.Fatalf("invited staff agent recursed: %v", r)
		}
	}
	if r := f.tool(false, "resume", map[string]any{"nonce": "local-lead-nonce", "resume_id": "local-rotation"}); r["token"] == "" {
		t.Fatalf("setup: rotation failed: %v", r)
	} else {
		token = r["token"].(string)
	}
	if f.rpc(true, "ping", map[string]any{})["error"] != nil {
		t.Fatal("ordinary issuer rotation revoked children")
	}
	if r := f.tool(false, "sign_off", map[string]any{"token": token}); r["ok"] != true {
		t.Fatalf("setup: close failed: %v", r)
	}
	// register CAN reopen a closed row on this path: prove the setup, then
	// prove that today's active status does not resurrect the earlier child.
	reopened := f.tool(false, "register", map[string]any{"name": "local-lead", "kind": "persistent", "nonce": "local-lead-nonce"})
	if reopened["token"] == nil {
		t.Fatalf("setup: issuer did not reopen: %v", reopened)
	}
	code, _ := f.post(f.public, "/mcp", map[string]string{"Authorization": "Bearer " + f.key}, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if code != 401 {
		t.Fatal("closed-then-reopened issuer resurrected old child")
	}
}

func TestCloudInvitationDibsIssuerNamespaceAndCoreSizedHost(t *testing.T) {
	f := newCloudFixture(t)
	parent := f.tool(false, "register", map[string]any{"name": "dibs-architect", "kind": "persistent"})
	token, _ := parent["token"].(string)
	if token == "" {
		t.Fatalf("setup: local issuer: %v", parent)
	}
	child := f.tool(false, "invite", map[string]any{"token": token})
	if child["name"] != "dibs-architect-cloud-1" || child["key"] == nil {
		t.Fatalf("existing dibs issuer could not invite its child: %v", child)
	}
	f.key = child["key"].(string)
	if r := f.tool(true, "register", map[string]any{"name": child["name"], "nonce": "dibs-child-retained"}); r["token"] == nil {
		t.Fatalf("authorized namespace failed registration: %v", r)
	}
	if code, _ := f.post(f.private, "/api/admin/role", map[string]string{
		"X-Dibs-Local": "board-secret", "X-Dibs-Admin": "operator-proof",
	},
		map[string]any{"agent": "dibs-architect", "role": "coordinator"}); code != 200 {
		t.Fatal("setup: coordinator grant failed")
	}
	for _, name := range []string{"dibs-anything", "human", "dibd", "admin", "coordinator", "local-staff"} {
		if r := f.tool(false, "invite", map[string]any{"token": token, "name": name}); r["code"] != "E_INVITE_SCOPE" {
			t.Fatalf("coordinator escaped reserved identities with %q: %v", name, r)
		}
	}
	maxName := core.DefaultLimits().MaxNameBytes - len(engine.InvitationHostPrefix)
	name := strings.Repeat("x", maxName)
	code, minted := f.admin("mint", name)
	if code != 200 {
		t.Fatalf("valid core-sized host refused: %d %v", code, minted)
	}
	f.key = minted["key"].(string)
	if r := f.tool(true, "register", map[string]any{"name": name, "nonce": "long-child-retained"}); r["token"] == nil {
		t.Fatalf("mint printed an unusable long-name recipe: %v", r)
	}
	code, refused := f.admin("mint", name+"x")
	if code == 200 || refused["hint"] == nil {
		t.Fatalf("mint exceeded invite host limit: %d %v", code, refused)
	}
}

// Re-exec the test binary to enter run(), not only startPublic(). A test which
// hand-wires a handler cannot detect the production call site disappearing.
func TestCloudPublicListenerIsWiredIntoDaemonRun(t *testing.T) {
	if os.Getenv("DIBS_CLOUD_TEST_CHILD") == "1" {
		os.Args = []string{"dibd", "--dir", os.Getenv("DIBS_CLOUD_TEST_DIR"), "--addr", os.Getenv("DIBS_CLOUD_TEST_PRIVATE"), "--public-url", os.Getenv("DIBS_CLOUD_TEST_URL"), "--public-addr", os.Getenv("DIBS_CLOUD_TEST_PUBLIC"), "--allow-parallel"}
		flag.CommandLine = cloudFlagSet()
		if err := run(); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	freeAddr := func() string {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		a := ln.Addr().String()
		if err = ln.Close(); err != nil {
			t.Fatal(err)
		}
		return a
	}
	private, public := freeAddr(), freeAddr()
	proxy := httptest.NewUnstartedServer(nil)
	target, err := url.Parse("http://" + public)
	if err != nil {
		t.Fatal(err)
	}
	proxy.Config.Handler = httputil.NewSingleHostReverseProxy(target)
	proxy.StartTLS()
	t.Cleanup(proxy.Close)
	key, err := (invites.Store{Dir: dir}).Mint("run-worker", time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCloudPublicListenerIsWiredIntoDaemonRun$")
	child.Env = append(os.Environ(), "DIBS_CLOUD_TEST_CHILD=1", "DIBS_CLOUD_TEST_DIR="+dir, "DIBS_CLOUD_TEST_PRIVATE="+private, "DIBS_CLOUD_TEST_PUBLIC="+public, "DIBS_CLOUD_TEST_URL="+proxy.URL)
	pipe, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = child.Stdout
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	up := make(chan struct{}, 1)
	var logs bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = fmt.Fprintln(&logs, line)
			if strings.Contains(line, "dibd up") {
				select {
				case up <- struct{}{}:
				default:
				}
			}
		}
	}()
	wait := make(chan error, 1)
	go func() { wait <- child.Wait() }()
	t.Cleanup(func() {
		_ = child.Process.Signal(os.Interrupt)
		select {
		case err := <-wait:
			if err != nil {
				t.Errorf("child daemon: %v", err)
			}
		case <-ctx.Done():
			t.Error("child daemon did not stop")
			cancel()
		}
		<-drained
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	select {
	case <-up:
	case <-ctx.Done():
		t.Fatal("daemon never reached real startup")
	}
	f := &cloudFixture{public: proxy, t: t, key: key}
	r := f.tool(true, "register", map[string]any{"name": "run-worker", "kind": "persistent", "nonce": "run-kept-nonce"})
	if r["agent_id"] != "run-worker" || r["token"] == "" {
		t.Fatalf("production startup never served public registration: %v", r)
	}
	plain := []byte("<html><script>localStorage.getItem('page_key')</script></html>")
	upload := authorizeTestUploadWithMime(f, r["token"].(string), plain, "text/html")
	response := patchTestUpload(t, proxy.Client(), upload, 0, plain, "?1")
	defer func() { _ = response.Body.Close() }()
	var stored map[string]any
	if err := json.NewDecoder(response.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || stored["blob"] == nil {
		t.Fatalf("production startup byte route missing: %d %v", response.StatusCode, stored)
	}
	down := f.tool(true, "download", map[string]any{"token": r["token"], "blob": stored["blob"]})
	descriptor, ok := down["download"].(map[string]any)
	if !ok {
		t.Fatalf("production download wiring: %v", down)
	}
	got, err := proxy.Client().Get(descriptor["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = got.Body.Close() }()
	assertDownloadIsData(t, got, "application/octet-stream")
	bytesOut, err := io.ReadAll(got.Body)
	if err != nil || got.StatusCode != 200 || !bytes.Equal(bytesOut, plain) {
		t.Fatalf("production download: %d %v", got.StatusCode, err)
	}
}

func TestCloudInvitationCannotCrossIdentityOrReadHubPaths(t *testing.T) {
	f := newCloudFixture(t)
	token := cloudRegistered(f)
	local := f.tool(false, "register", map[string]any{"name": "local-worker", "kind": "persistent", "nonce": "local-kept-nonce"})
	localToken, _ := local["token"].(string)
	if localToken == "" {
		t.Fatal("setup: local registration failed")
	}
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"register", map[string]any{"name": "someone-else", "nonce": "unrelated-nonce"}},
		{"register", map[string]any{"name": "cloud-worker", "nonce": "sibling-nonce"}},
		{"register", map[string]any{"name": "cloud-worker", "nonce": "local-kept-nonce"}},
		{"resume", map[string]any{"nonce": "local-kept-nonce", "resume_id": "cloud-activation-1"}},
		{"inbox", map[string]any{"token": localToken}},
		{"declare", map[string]any{"token": localToken, "text": "stolen"}},
		{"update", map[string]any{"token": token, "name": "renamed"}},
		{"bind_session", map[string]any{"token": token, "session_id": "local-thread"}},
		{"human_unlock", map[string]any{"token": token}},
	} {
		r := f.tool(true, c.name, c.args)
		if r["code"] != "E_INVITE_SCOPE" || r["hint"] == "" {
			t.Fatalf("%s was not scoped: %v", c.name, r)
		}
	}
	out := f.rpc(true, "resources/read", map[string]any{"uri": "dibs://inbox", "_meta": map[string]any{"com.dibs/token": localToken}})
	if out["error"] == nil {
		t.Fatal("foreign resource token accepted")
	}
	hubFile := filepath.Join(f.dir, "hub-private.txt")
	if err := os.WriteFile(hubFile, []byte("hub-private-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := f.tool(true, "put_blob", map[string]any{"token": token, "path": hubFile})
	if r["code"] != "E_INVITE_SCOPE" || !strings.Contains(r["hint"].(string), "base64") {
		t.Fatal("invite could read a real file on the hub")
	}
	data := bytes.Repeat([]byte("large cloud bytes"), 20000)
	r = f.tool(true, "put_blob", map[string]any{"token": token, "data": base64.StdEncoding.EncodeToString(data)})
	id, _ := r["blob"].(string)
	if id == "" {
		t.Fatalf("inline upload: %v", r)
	}
	out = f.rpc(true, "tools/call", map[string]any{"name": "get_blob", "arguments": map[string]any{"token": token, "blob": id, "as": "auto"}})
	content := out["result"].(map[string]any)["content"].([]any)
	resource, _ := content[1].(map[string]any)["resource"].(map[string]any)
	if resource["blob"] != base64.StdEncoding.EncodeToString(data) || bytes.Contains(mustJSON(t, out), []byte(`"path"`)) {
		t.Fatal("auto download returned a hub-local path instead of own bytes")
	}
	if r = f.tool(true, "get_blob", map[string]any{"token": token, "blob": id, "as": "path"}); r["code"] != "E_INVITE_SCOPE" {
		t.Fatal("explicit hub path accepted")
	}
}

func TestCloudInvitationListenerNeverPublishesPrivateRoutes(t *testing.T) {
	f := newCloudFixture(t)
	for _, path := range []string{"/", "/events", "/api/board", "/api/admin/invites", "/api/human/enroll", "/api/host/wake", "/healthz", "/mcp/", "/mcp?bt=x", "/x/../api/board", "/%61pi/board"} {
		if code, out := f.post(f.public, path, map[string]string{"Authorization": "Bearer " + f.key}, map[string]any{}); code != 403 || out["hint"] == "" {
			t.Fatalf("public path %s: %d %v", path, code, out)
		}
	}
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}
	if code, _ := f.post(f.public, "/mcp", map[string]string{"Authorization": "Bearer board-secret"}, payload); code != 401 {
		t.Fatal("board secret entered public listener")
	}
	if code, _ := f.post(f.private, "/mcp", map[string]string{"Authorization": "Bearer " + f.key}, payload); code != 401 {
		t.Fatal("invite entered private listener")
	}
	if code, _ := f.post(f.private, "/api/admin/invites", map[string]string{"Authorization": "Bearer board-secret"}, map[string]any{"action": "mint", "name": "unauthorized"}); code == 200 {
		t.Fatal("board secret alone minted invitation without human proof")
	}
	if code, _ := f.post(f.public, "/mcp", map[string]string{"Authorization": "Bearer " + f.key, "Origin": "https://evil.example"}, payload); code != 403 {
		t.Fatal("foreign origin accepted")
	}
	if code, _ := f.post(f.public, "/mcp", map[string]string{"Authorization": "Bearer " + f.key, "Origin": f.public.URL}, payload); code != 200 {
		t.Fatal("configured public origin refused")
	}
	for _, method := range []string{"subscriptions/listen", "resources/subscribe"} {
		if f.rpc(true, method, map[string]any{})["error"] == nil {
			t.Fatal("public wake/subscription route accepted")
		}
	}
	for _, name := range []string{"human", "dibs-staff", "local-staff"} {
		if code, _ := f.admin("mint", name); code == 200 {
			t.Fatalf("privileged name %q invited", name)
		}
	}
	if code, list := f.admin("list", ""); code != 200 || bytes.Contains(mustJSON(t, list), []byte(f.key)) || bytes.Contains(mustJSON(t, list), []byte(`"digest"`)) {
		t.Fatal("list exposed a credential/verifier or failed")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
