package main

import (
	"encoding/json"
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
