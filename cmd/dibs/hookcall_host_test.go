package main

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/mcp"
)

// A hook that reaches the daemon without the stdio bridge still says which
// machine it is on.
//
// The host-scoped lookups (guard_path, hook_poll) take the machine from
// `_meta com.dibs/host`, and a call that carries none is stamped with the
// daemon's own identity when it arrives on loopback. The `dibs hook-poll`
// and `dibs hook` subcommands post their own JSON-RPC and carried no meta at
// all, so through the documented `ssh -L` forward a remote agent's
// registration recorded its machine while its guard arrived as the hub's:
// the lookup matched nothing and the edit went ahead past an exclusive
// claim. The stdio bridge stamps the host on every call; so does this.
// Round seventeen of the pre-release review.
func TestAHookCallCarriesItsHost(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_HOST_ID", "machine-b")
	hostIDOnce, hostIDValue = sync.Once{}, ""
	t.Cleanup(func() { hostIDOnce, hostIDValue = sync.Once{}, "" })

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"agent\":\"x\"}"}]}}`))
	}))
	defer srv.Close()
	t.Setenv("DIBS_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	mcpEndpointOnce, mcpEndpointValue = sync.Once{}, ""
	t.Cleanup(func() { mcpEndpointOnce, mcpEndpointValue = sync.Once{}, "" })

	var out struct {
		Agent string `json:"agent"`
	}
	if err := callHookTool("hook_poll", map[string]any{"session_id": "host-1", "event": "SessionStart"}, &out); err != nil {
		t.Fatalf("setup: the hook call failed: %v", err)
	}
	params, _ := got["params"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	if host, _ := meta[mcp.HostMetaKey].(string); host != "machine-b" {
		t.Fatalf("the hook call carried host %q in %v: a daemon reached through a forward stamps "+
			"it as its own machine and the host-scoped lookup resolves nobody", host, params)
	}
}

// And it spells its paths as the bridge does: resolved, not as the harness
// gave them. A hub on another machine does not resolve a remote caller's
// paths, so a hook announcing `/tmp/repo` never matched a registration the
// bridge had recorded as `/private/tmp/repo`, and reported success while
// delivering nothing. Round twenty-two of the pre-release review.
func TestAHookCallSpellsPathsAsTheBridgeDoes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_HOST_ID", "machine-b")
	hostIDOnce, hostIDValue = sync.Once{}, ""
	t.Cleanup(func() { hostIDOnce, hostIDValue = sync.Once{}, "" })
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}]}}`))
	}))
	defer srv.Close()
	t.Setenv("DIBS_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	mcpEndpointOnce, mcpEndpointValue = sync.Once{}, ""
	t.Cleanup(func() { mcpEndpointOnce, mcpEndpointValue = sync.Once{}, "" })
	var out map[string]any
	if err := callHookTool("hook_poll", map[string]any{"session_id": "s", "event": "SessionStart", "cwd": alias}, &out); err != nil {
		t.Fatalf("setup: %v", err)
	}
	params, _ := got["params"].(map[string]any)
	args, _ := params["arguments"].(map[string]any)
	if cwd, _ := args["cwd"].(string); cwd != resolved {
		t.Fatalf("the hook announced cwd %q, want %q as the bridge registered it", cwd, resolved)
	}
}

// And so does the opencode plugin, which posts its own JSON-RPC from
// opencode's plugin runtime and never passes through the bridge.
//
// The real file is imported under bun and its guard hook is driven at a
// fake daemon that records what arrived. The plugin's host is read the way
// the bridge reads it (DIBS_HOST_ID, then the data directory), so the two
// halves of one machine agree.
func TestTheOpencodePluginCarriesItsHost(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed; the plugin cannot be run")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// What the bridge resolved wins over what the daemon's file says: on a
	// Supgang member the two differ, and the bridge's answer is the one on
	// the registration.
	if err := os.WriteFile(filepath.Join(dir, resolvedHostFile), []byte("machine-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_id"), []byte("not-what-the-bridge-said\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &got)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"decision\":\"allow\"}"}]}}`))
	}))
	defer srv.Close()

	plugin, err := filepath.Abs(filepath.Join("..", "..", "plugins", "opencode", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	script := "const { DibsPlugin } = await import(" + strconv.Quote(plugin) + ")\n" +
		"const hooks = await DibsPlugin({} as any)\n" +
		"await hooks[\"tool.execute.before\"]!({ tool: \"edit\", sessionID: \"s\", callID: \"c\" } as any, " +
		"{ args: { filePath: \"/w/repo/file.go\" } } as any)\n"
	path := filepath.Join(dir, "drive.ts")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bun, "run", path) // #nosec G204 -- paths this test created
	cmd.Env = append(os.Environ(),
		"DIBS_DIR="+dir, "DIBS_ADDR="+strings.TrimPrefix(srv.URL, "http://"), "DIBS_HOST_ID=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running the plugin: %v\n%s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	params, _ := got["params"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	if host, _ := meta[mcp.HostMetaKey].(string); host != "machine-b" {
		t.Fatalf("the plugin's guard carried host %q in %v: through a forward the daemon stamps "+
			"it as its own machine and the guard resolves nobody", host, got)
	}
}

// The bridge publishes the host it resolved, so the plugin can read it.
func TestTheResolvedHostIsPublishedForThePlugin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_HOST_ID", "machine-b")
	hostIDOnce, hostIDValue = sync.Once{}, ""
	t.Cleanup(func() { hostIDOnce, hostIDValue = sync.Once{}, "" })
	if got := hostID(); got != "machine-b" {
		t.Fatalf("setup: hostID() = %q", got)
	}
	b, err := os.ReadFile(filepath.Join(dir, resolvedHostFile))
	if err != nil || strings.TrimSpace(string(b)) != "machine-b" {
		t.Fatalf("the resolved host was not published for the plugin: %q, %v", b, err)
	}
}

// And it spells paths as the daemon compares them: resolved, not as typed.
//
// The daemon resolves symlinks only for a caller on its own machine, so a
// plugin on another machine that sent `/tmp/review/new.go` was compared
// against a claim its bridge had stored as `/private/tmp/review/new.go`,
// and the exclusive claim did not cover the edit. Round nineteen of the
// pre-release review. The file being written need not exist yet.
func TestTheOpencodePluginSendsCanonicalPaths(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed; the plugin cannot be run")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &got)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"decision\":\"allow\"}"}]}}`))
	}))
	defer srv.Close()
	plugin, err := filepath.Abs(filepath.Join("..", "..", "plugins", "opencode", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	// The edit names a file that does not exist yet, through the alias.
	edit := filepath.Join(alias, "new.go")
	script := "const { DibsPlugin } = await import(" + strconv.Quote(plugin) + ")\n" +
		"const hooks = await DibsPlugin({} as any)\n" +
		"await hooks[\"tool.execute.before\"]!({ tool: \"write\", sessionID: \"s\", callID: \"c\" } as any, " +
		"{ args: { filePath: " + strconv.Quote(edit) + " } } as any)\n"
	path := filepath.Join(dir, "drive.ts")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bun, "run", path) // #nosec G204 -- paths this test created
	cmd.Dir = alias
	cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_ADDR="+strings.TrimPrefix(srv.URL, "http://"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running the plugin: %v\n%s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	params, _ := got["params"].(map[string]any)
	args, _ := params["arguments"].(map[string]any)
	if p, _ := args["path"].(string); p != filepath.Join(resolved, "new.go") {
		t.Fatalf("the guard was asked about %q, want the resolved %q: a claim stored resolved does "+
			"not cover the alias on a hub that will not resolve a remote path", p, filepath.Join(resolved, "new.go"))
	}
	if c, _ := args["cwd"].(string); c != resolved {
		t.Fatalf("the guard's cwd is %q, want the resolved %q", c, resolved)
	}
}

// And it reaches a joined board: an https:// DIBS_ADDR as `dibs mcp-config`
// writes it, under the certificate `dibs trust` recorded.
//
// The plugin prefixed `http://` to whatever DIBS_ADDR held, so a joined
// board became `http://https://hub:4777/mcp` and every hook failed silently
// while the bridge beside it connected; and a hub off loopback serves a
// certificate it issued itself, which the plugin had no way to trust.
// Round nineteen of the pre-release review.
func TestTheOpencodePluginReachesAJoinedBoardOverTLS(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed; the plugin cannot be run")
	}
	// The two files the bridge trusts: a recorded peer, and the daemon's
	// own CA. Reading only the first left a hub's own plugin refusing the
	// daemon its bridge accepted. Round twenty of the pre-release review.
	for _, store := range []string{"trusted-certs.pem", "tls-ca.pem"} {
		t.Run(store, func(t *testing.T) { opencodePluginReachesTLS(t, bun, store) })
	}
}

func opencodePluginReachesTLS(t *testing.T, bun, store string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	reached := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reached++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"decision\":\"deny\",\"reason\":\"held\"}"}]}}`))
	}))
	defer srv.Close()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, store), pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	plugin, err := filepath.Abs(filepath.Join("..", "..", "plugins", "opencode", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	// The guard's deny is the proof the answer came from the server: a
	// plugin that could not reach it allows, by design.
	script := "const { DibsPlugin } = await import(" + strconv.Quote(plugin) + ")\n" +
		"const hooks = await DibsPlugin({} as any)\n" +
		"try {\n" +
		"  await hooks[\"tool.execute.before\"]!({ tool: \"edit\", sessionID: \"s\", callID: \"c\" } as any, " +
		"{ args: { filePath: \"/w/repo/file.go\" } } as any)\n" +
		"  console.log(\"allowed\")\n" +
		"} catch (e) { console.log(\"denied: \" + String(e)) }\n"
	path := filepath.Join(dir, "drive.ts")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bun, "run", path) // #nosec G204 -- paths this test created
	cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_ADDR="+srv.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running the plugin: %v\n%s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if reached == 0 || !strings.Contains(string(out), "denied: Error: Dibs: held") {
		t.Fatalf("the plugin did not reach the joined board at %s (reached %d, said %q): its hooks fail "+
			"silently while the bridge beside it connects", srv.URL, reached, out)
	}
}

// And pi's transport reaches a joined board under NODE, which is where pi
// runs: Node's fetch is undici and ignores a `tls` option, so the option
// that works under bun trusted nothing there and pi installed no tools.
// The real post() is lifted from the extension and run under every runtime
// present, against a server whose certificate only the data directory
// vouches for. Round twenty of the pre-release review.
func TestThePiTransportReachesAJoinedBoardUnderNode(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "pi", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "async function post("
	start := strings.Index(string(src), marker)
	if start < 0 {
		t.Fatal("plugins/pi/dibs.ts no longer defines post(): the transport was renamed or removed")
	}
	end := strings.Index(string(src)[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of post()")
	}
	fn := string(src)[start : start+end+3]

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"reached":true}}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "tls-ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	script := fn + "\nconst ca = (await import(\"node:fs/promises\")).readFile(process.env.CA!, \"utf8\")\n" +
		"const text = await post(process.env.URL! + \"/mcp\", \"{}\", { \"content-type\": \"application/json\" }, 3000, [await ca])\n" +
		"console.log(text ?? \"NULL\")\n"
	path := filepath.Join(dir, "post.ts")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, runtime := range [][]string{{"node", "--experimental-strip-types", path}, {"bun", "run", path}} {
		bin, err := exec.LookPath(runtime[0])
		if err != nil {
			continue
		}
		ran++
		cmd := exec.Command(bin, runtime[1:]...) // #nosec G204 -- paths this test created
		cmd.Env = append(os.Environ(), "CA="+ca, "URL="+srv.URL, "NODE_NO_WARNINGS=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), `"reached":true`) {
			t.Errorf("%s: the transport did not reach the board under the data directory's CA: %v\n%s", runtime[0], err, out)
		}
	}
	if ran == 0 {
		t.Skip("neither node nor bun is installed")
	}
}

// And pi's extension stamps the host on every tool call, as the bridge does.
//
// The extension is pi's whole MCP client (it registers directly, no bridge),
// so a call without `_meta com.dibs/host` from another machine registered
// an agent with no machine, or through an ssh forward with the hub's, and
// the daemon then never chose that machine's host bridge for a wake.
// Round twenty-one of the pre-release review. The real file is copied
// beside a stub of its one runtime dependency and driven through its
// before_agent_start hook at a fake daemon that records what arrived.
//
// AND THE CHECKOUT, AND FROM A FRESH DIRECTORY. The extension asked nothing
// about the repository, so a remote pi agent registered with no checkout
// and two clones of one repository on two machines were both granted an
// exclusive claim on the same tracked file; and it read the host from files
// only a bridge writes, so a joined directory holding just the secret and
// the trust store registered with no machine at all. It now asks `dibs
// identity`, the same binary the bridge is, which this test binary stands
// in for (TestMain). The data directory here holds nothing but the secret:
// the binary mints the host as the bridge would, and reads the checkout
// from Git. Round twenty-two of the pre-release review.
func TestThePiExtensionCarriesItsHost(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed; the extension cannot be run")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Two checkouts of two projects: the one the extension runs in, and
	// the one an `update` moves the agent to; and an alias for the first,
	// through which a claim is made (round twenty-four: claim paths went
	// out as typed, and a hub does not resolve a remote caller's paths).
	checkout, other := filepath.Join(dir, "checkout"), filepath.Join(dir, "other")
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(checkout, alias); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	for _, c := range []string{checkout, other} {
		if err := os.Mkdir(c, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://example.invalid/t/" + filepath.Base(c) + ".git"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = c
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("setup: git %v: %v: %s", args, err, out)
			}
		}
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "pi", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dibs.ts"), src, 0o600); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(dir, "node_modules", "typebox")
	if err := os.MkdirAll(stub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stub, "index.ts"),
		[]byte("export const Type = { Unsafe: (s: unknown) => s }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		mu.Lock()
		calls = append(calls, got)
		mu.Unlock()
		if got["method"] == "tools/list" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"register","inputSchema":{"type":"object"}},{"name":"update","inputSchema":{"type":"object"}},{"name":"claim","inputSchema":{"type":"object"}}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"token\":\"t\"}"}]}}`))
	}))
	defer srv.Close()
	// The hook, then the register tool the extension fetched, executed as
	// pi would execute it.
	script := `const mod = await import("./dibs.ts")
const handlers: Record<string, Function> = {}
const tools: Record<string, any> = {}
mod.default({ on: (name: string, fn: Function) => { handlers[name] = fn }, registerTool: (t: any) => { tools[t.label] = t }, registerCommand: () => {} } as any)
const ctx = { sessionManager: { getSessionId: () => "pi-session-1" } }
await handlers["before_agent_start"]!({}, ctx)
await tools["register"].execute("c1", { name: "probe" }, undefined, undefined, ctx)
await tools["update"].execute("c2", { token: "t", cwd: process.env["OTHER"] }, undefined, undefined, ctx)
await tools["claim"].execute("c3", { token: "t", path: process.env["CLAIM"], mode: "exclusive" }, undefined, undefined, ctx)
`
	if err := os.WriteFile(filepath.Join(dir, "drive.ts"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bun, "run", filepath.Join(dir, "drive.ts")) // #nosec G204 -- paths this test created
	cmd.Dir = checkout
	cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_ADDR="+strings.TrimPrefix(srv.URL, "http://"),
		"DIBS_BIN="+os.Args[0], "DIBS_TEST_AS_DIBS=1", "DIBS_HOST_ID=", "OTHER="+other,
		"CLAIM="+filepath.Join(alias, "pkg", "x.go"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running the extension: %v\n%s", err, out)
	}
	// Nothing named the machine: the binary minted an id for this fresh
	// directory, as the bridge would have, and that is the one to expect.
	minted, err := os.ReadFile(filepath.Join(dir, "host_id"))
	if err != nil || strings.TrimSpace(string(minted)) == "" {
		t.Fatalf("no host id was minted in the fresh directory: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	stamped, registered, updated, claimed := 0, false, false, false
	for _, c := range calls {
		if c["method"] != "tools/call" {
			continue
		}
		params, _ := c["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		if host, _ := meta[mcp.HostMetaKey].(string); host != strings.TrimSpace(string(minted)) {
			t.Fatalf("a tools/call from the extension carried host %q in %v, want the id minted for "+
				"this directory: from a fresh joined directory the extension registered with no machine", host, params)
		}
		stamped++
		if params["name"] == "claim" {
			claimed = true
			args, _ := params["arguments"].(map[string]any)
			resolved, _ := filepath.EvalSymlinks(checkout)
			if p, _ := args["path"].(string); p != filepath.Join(resolved, "pkg", "x.go") {
				t.Fatalf("the claim went out as %q, want %q: a hub does not resolve a remote caller's "+
					"paths, so this claim has no repository-relative key and collides with nothing", p,
					filepath.Join(resolved, "pkg", "x.go"))
			}
			continue
		}
		// The checkout each call describes is the one it names: the
		// process's own on register, the target's on an update that moves
		// the agent. Round twenty-three: the update carried the original
		// checkout's identity beside the new directory.
		var in string
		switch params["name"] {
		case "register":
			registered, in = true, checkout
		case "update":
			updated, in = true, other
		default:
			continue
		}
		repo, _ := meta[mcp.RepoMetaKey].(map[string]any)
		wantRemote := "example.invalid/t/" + filepath.Base(in)
		if remote, _ := repo["remote"].(string); remote != wantRemote {
			t.Fatalf("the %s carried checkout identity %q in %v, want %q: a hub records the wrong "+
				"repository (or none) for it, and two clones of one repository on two machines both "+
				"get an exclusive claim on the same file", params["name"], remote, params, wantRemote)
		}
		args, _ := params["arguments"].(map[string]any)
		want, _ := filepath.EvalSymlinks(in)
		if cwd, _ := args["cwd"].(string); cwd != want {
			t.Fatalf("the %s's cwd is %q, want %q as the bridge would spell it", params["name"], cwd, want)
		}
	}
	if stamped == 0 || !registered || !updated || !claimed {
		t.Fatalf("the drive made %d tools/call(s), registered=%v updated=%v claimed=%v: %v",
			stamped, registered, updated, claimed, calls)
	}
}

// And its transport gives up on a response that never finishes.
//
// post() bounded the socket's IDLE time, so a daemon that kept trickling
// bytes was never cut off, and before_agent_start awaits the poll in front
// of the user's turn: a stalled daemon held the turn open indefinitely.
// The bound is elapsed time now. Round twenty-four of the pre-release
// review. The lifted helper is run against a server that never stops.
func TestThePiTransportGivesUpOnATricklingResponse(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "pi", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "async function post("
	start := strings.Index(string(src), marker)
	if start < 0 {
		t.Fatal("plugins/pi/dibs.ts no longer defines post()")
	}
	end := strings.Index(string(src)[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of post()")
	}
	fn := string(src)[start : start+end+3]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		for i := 0; i < 100; i++ { // ten seconds of trickle, well past the bound
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			_, _ = w.Write([]byte("x"))
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	script := fn + "\nconst t0 = Date.now()\n" +
		"const text = await post(process.env.URL! + \"/mcp\", \"{}\", {}, 300, null)\n" +
		"console.log(JSON.stringify({ text, ms: Date.now() - t0 }))\n"
	path := filepath.Join(dir, "post.ts")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, runtime := range [][]string{{"node", "--experimental-strip-types", path}, {"bun", "run", path}} {
		bin, err := exec.LookPath(runtime[0])
		if err != nil {
			continue
		}
		ran++
		cmd := exec.Command(bin, runtime[1:]...) // #nosec G204 -- paths this test created
		cmd.Env = append(os.Environ(), "URL="+srv.URL, "NODE_NO_WARNINGS=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", runtime[0], err, out)
		}
		var got struct {
			Text *string `json:"text"`
			MS   int     `json:"ms"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &got); err != nil {
			t.Fatalf("%s: %v: %s", runtime[0], err, out)
		}
		if got.Text != nil || got.MS > 2000 {
			t.Errorf("%s: a 300ms bound let a trickling response run %dms and answer %v: the user's turn waits on it",
				runtime[0], got.MS, got.Text)
		}
	}
	if ran == 0 {
		t.Skip("neither node nor bun is installed")
	}
}

// And it picks up the host the bridge publishes after its first call.
//
// A hook can run before the bridge has published `resolved_host_id`, and
// the plugin cached what it found then for the life of the process: "" (or
// the daemon's own file), never the bridge's eventual answer. Through an
// ssh forward an empty assertion is the hub's identity, so every later
// guard resolved nobody and allowed the edit. Round twenty-three of the
// pre-release review.
func TestTheOpencodePluginPicksUpTheHostTheBridgePublishesLater(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed; the plugin cannot be run")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var hosts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		params, _ := got["params"].(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		host, _ := meta[mcp.HostMetaKey].(string)
		mu.Lock()
		hosts = append(hosts, host)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"decision\":\"allow\"}"}]}}`))
	}))
	defer srv.Close()
	plugin, err := filepath.Abs(filepath.Join("..", "..", "plugins", "opencode", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	published := filepath.Join(dir, resolvedHostFile)
	script := "const { DibsPlugin } = await import(" + strconv.Quote(plugin) + ")\n" +
		"const hooks = await DibsPlugin({} as any)\n" +
		"const edit = () => hooks[\"tool.execute.before\"]!({ tool: \"edit\", sessionID: \"s\", callID: \"c\" } as any, " +
		"{ args: { filePath: \"/w/repo/file.go\" } } as any)\n" +
		"await edit()\n" + // before the bridge has published anything
		"await Bun.write(" + strconv.Quote(published) + ", \"machine-b\\n\")\n" +
		"await edit()\n"
	path := filepath.Join(dir, "drive.ts")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bun, "run", path) // #nosec G204 -- paths this test created
	cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_ADDR="+strings.TrimPrefix(srv.URL, "http://"), "DIBS_HOST_ID=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running the plugin: %v\n%s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hosts) != 2 || hosts[0] != "" || hosts[1] != "machine-b" {
		t.Fatalf("the guard's hosts across the bridge publishing its answer were %q, want [\"\" \"machine-b\"]: "+
			"the first answer was kept for the life of the process", hosts)
	}
}

// Both plugins dial the hub where it is NOW, not where the config was
// printed. A joined board's config may name the hub as a Supgang peer; the
// bridge re-resolves it on every start, and the plugins derived their
// endpoint from the saved DIBS_ADDR alone, so after the hub moved they went
// on dialling the old address: opencode lost delivery and failed its guard
// open, pi's calls failed. opencode reads the origin the bridge published
// beside the secret; pi takes it from `dibs identity`. Round twenty-six of
// the pre-release review. DIBS_ADDR here names a port nothing listens on.
func TestThePluginsFollowTheHubWhereItIsNow(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed")
	}
	var mu sync.Mutex
	reached := map[string]int{}
	now := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		mu.Lock()
		reached[fmt.Sprint(got["method"])]++
		mu.Unlock()
		if got["method"] == "tools/list" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"decision\":\"deny\",\"reason\":\"held\"}"}]}}`))
	}))
	defer now.Close()
	const stale = "127.0.0.1:9" // the address the config was printed with; nothing answers

	t.Run("opencode", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("s3cret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// What the bridge published when it started beside this plugin.
		if err := os.WriteFile(filepath.Join(dir, resolvedOriginFile), []byte(now.URL+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		plugin, _ := filepath.Abs(filepath.Join("..", "..", "plugins", "opencode", "dibs.ts"))
		script := "const { DibsPlugin } = await import(" + strconv.Quote(plugin) + ")\n" +
			"const hooks = await DibsPlugin({} as any)\n" +
			"try { await hooks[\"tool.execute.before\"]!({ tool: \"edit\", sessionID: \"s\", callID: \"c\" } as any, " +
			"{ args: { filePath: \"/w/repo/file.go\" } } as any); console.log(\"allowed\") } catch (e) { console.log(\"denied\") }\n"
		path := filepath.Join(dir, "drive.ts")
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bun, "run", path) // #nosec G204 -- paths this test created
		cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_ADDR="+stale, "DIBS_HOST_ID=")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "denied") {
			t.Fatalf("the guard did not reach the hub where it is now (%s): %v %s", now.URL, err, out)
		}
	})

	t.Run("pi", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("s3cret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "pi", "dibs.ts"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "dibs.ts"), src, 0o600); err != nil {
			t.Fatal(err)
		}
		stub := filepath.Join(dir, "node_modules", "typebox")
		if err := os.MkdirAll(stub, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stub, "index.ts"), []byte("export const Type = { Unsafe: (s: unknown) => s }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		script := `const mod = await import("./dibs.ts")
const handlers: Record<string, Function> = {}
mod.default({ on: (name: string, fn: Function) => { handlers[name] = fn }, registerTool: () => {}, registerCommand: () => {} } as any)
await handlers["before_agent_start"]!({}, { sessionManager: { getSessionId: () => "pi-session-1" } })
`
		if err := os.WriteFile(filepath.Join(dir, "drive.ts"), []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		before := reached["tools/call"]
		mu.Unlock()
		cmd := exec.Command(bun, "run", filepath.Join(dir, "drive.ts")) // #nosec G204 -- paths this test created
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_ADDR="+stale, "DIBS_HOST_ID=machine-b",
			"DIBS_BIN="+os.Args[0], "DIBS_TEST_AS_DIBS=1", "DIBS_TEST_IDENTITY_ORIGIN="+now.URL)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("running the extension: %v\n%s", err, out)
		}
		mu.Lock()
		defer mu.Unlock()
		if reached["tools/call"] == before {
			t.Fatalf("pi's poll never reached the hub where it is now (%s): it dialled the saved address", now.URL)
		}
	})
}

// Both plugins spell a path beneath a top-level directory that does not
// exist yet the way it was written.
//
// canonical() resolves the deepest existing ancestor and re-attaches the
// rest, and it took the rest by slicing off the parent's length plus a
// separator. Under "/" the parent is one character, so the slice ate the
// first letter of the name: `/srv/new.go` became `/rv/new.go`, and the
// guard then asked about a path nobody was writing while the bridge had
// recorded the claim correctly. Round twenty-seven of the pre-release
// review. The real function is lifted from each plugin and run.
func TestThePluginsSpellPathsUnderAMissingTopLevelDirectory(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed")
	}
	dir := t.TempDir()
	// A top-level name that does not exist: resolution walks to "/".
	const missing = "/dibs-review-missing-root-27/pkg/new.go"
	for _, plugin := range []string{"opencode", "pi"} {
		t.Run(plugin, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("..", "..", "plugins", plugin, "dibs.ts"))
			if err != nil {
				t.Fatal(err)
			}
			const marker = "function canonical(p: string): string {"
			start := strings.Index(string(src), marker)
			if start < 0 {
				t.Fatalf("plugins/%s/dibs.ts no longer defines canonical()", plugin)
			}
			end := strings.Index(string(src)[start:], "\n}\n")
			if end < 0 {
				t.Fatal("could not find the end of canonical()")
			}
			fn := string(src)[start : start+end+3]
			script := "import { basename, dirname, isAbsolute, join, resolve } from \"node:path\"\n" +
				"import { realpathSync } from \"node:fs\"\n" + fn +
				"\nconsole.log(canonical(process.env.P!))\n"
			path := filepath.Join(dir, plugin+".ts")
			if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bun, "run", path) // #nosec G204 -- paths this test created
			cmd.Env = append(os.Environ(), "P="+missing)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != missing {
				t.Fatalf("canonical(%q) = %q: the guard asks about a path nobody is writing, and a claim "+
					"the bridge recorded correctly is missed", missing, got)
			}
		})
	}
}

// And the opencode plugin follows the hub when the bridge republishes
// MID-SESSION, which is when a hub moves or the bridge restarts.
//
// The origin was read once and kept for the life of the process, so the
// plugin went on dialling an address the bridge had already left: the same
// defect round twenty-six closed, one step later. Round twenty-seven of
// the pre-release review.
func TestTheOpencodePluginFollowsTheHubWhenTheBridgeRepublishes(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	hits := map[string]int{}
	mk := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[name]++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"decision\":\"allow\"}"}]}}`))
		}))
	}
	first, second := mk("first"), mk("second")
	defer first.Close()
	defer second.Close()
	if err := os.WriteFile(filepath.Join(dir, resolvedOriginFile), []byte(first.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin, err := filepath.Abs(filepath.Join("..", "..", "plugins", "opencode", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	// One guard, then the bridge republishes (the hub moved), then another.
	script := "const { DibsPlugin } = await import(" + strconv.Quote(plugin) + ")\n" +
		"const hooks = await DibsPlugin({} as any)\n" +
		"const edit = () => hooks[\"tool.execute.before\"]!({ tool: \"edit\", sessionID: \"s\", callID: \"c\" } as any, " +
		"{ args: { filePath: \"/w/repo/file.go\" } } as any)\n" +
		"await edit()\n" +
		"await Bun.write(" + strconv.Quote(filepath.Join(dir, resolvedOriginFile)) + ", process.env.SECOND + \"\\n\")\n" +
		"await Bun.sleep(1100)\n" + // past the re-read window; a hub moving is not a hot path
		"await edit()\n"
	path := filepath.Join(dir, "drive.ts")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bun, "run", path) // #nosec G204 -- paths this test created
	cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_ADDR=127.0.0.1:9", "DIBS_HOST_ID=", "SECOND="+second.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running the plugin: %v\n%s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["first"] == 0 || hits["second"] == 0 {
		t.Fatalf("guards reached %v: after the bridge republished, the plugin went on dialling the "+
			"address it read first", hits)
	}
}
