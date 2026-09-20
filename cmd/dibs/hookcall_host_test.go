package main

import (
	"encoding/json"
	"encoding/pem"
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
