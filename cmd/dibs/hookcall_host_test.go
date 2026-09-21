package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/supgang"
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

// The plugins used to be tested here, against a fake HTTP daemon, because
// they used to BE clients: five suites drove `plugins/opencode/dibs.ts`
// under bun and asserted it stamped the same host, resolved the same
// paths, trusted the same certificates and followed the same hub as the
// bridge did. They are gone with the duplication they policed. Both
// plugins now speak JSON-RPC to `dibs mcp-stdio`, so agreement is not a
// property to check: there is one implementation, and it is the one
// tested above and in cmd/dibs/trust_test.go.
//
// What is left of those properties is tested where it can now fail.
// internal/mcp/e2e/guard_e2e.ts drives the real plugin against a REAL
// daemon, which is a fixture a transport can actually be wrong in front
// of: a deny reaching the harness, a claim matched through a symlink, a
// new file under a symlinked path, and the session id the bridge
// registered being the one the plugin's guard resolves.
// TestThePluginsHoldNoClientPolicy is what stops the copies growing back.
// A machine known by its Supgang id keeps it when a lookup fails.
//
// hostID fell through to the id it mints in the data directory on ANY
// error: a timeout, a restarting service, a process that started while
// Supgang was initialising. That bridge then held the minted id for its
// life while its neighbours held the Supgang one, and the fold reads two
// ids as two machines: two agents on one computer each took an exclusive
// claim on the same path outside a checkout, and the guard allowed both
// writes. Round twenty-eight of the pre-release review.
func TestAFailedSupgangLookupDoesNotRenameThisMachine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_HOST_ID", "")
	reset := func() {
		hostIDOnce, hostIDValue = sync.Once{}, ""
	}
	t.Cleanup(reset)

	reset()
	useFakeSupgang(t)
	fleet := hostID()
	if len(fleet) != 64 {
		t.Fatalf("setup: Supgang's answer was %q, want a node id", fleet)
	}

	// The same machine, a moment later, with Supgang not answering.
	reset()
	old := supgang.Command
	supgang.Command = "/nonexistent/supgang-down"
	t.Cleanup(func() { supgang.Command = old })
	if got := hostID(); got != fleet {
		t.Fatalf("with Supgang down this machine calls itself %q, having been %q: its own agents "+
			"read as two machines, so two of them take an exclusive claim on one path", got, fleet)
	}

	// A machine that has never resolved has nothing to remember, and every
	// process there agrees on the minted id.
	fresh := t.TempDir()
	t.Setenv("DIBS_DIR", fresh)
	reset()
	first := hostID()
	reset()
	if second := hostID(); first == "" || second != first {
		t.Fatalf("a machine with no Supgang answer minted %q then %q", first, second)
	}

	// AND SUPGANG BECOMING AVAILABLE DOES NOT RENAME IT EITHER, which is
	// the same split from the other side: a bridge started after Supgang
	// came up answered with the fleet id while every older bridge, and the
	// running daemon, still answered with the minted one, so two agents on
	// one computer could each take an exclusive claim on one path. The
	// machine adopts its fleet identity when the daemon there next starts
	// and remembers it. Round thirty-three of the pre-release review.
	useFakeSupgang(t)
	reset()
	if got := hostID(); got != first {
		t.Fatalf("with Supgang now answering this machine calls itself %q, having been %q "+
			"a moment ago: its own bridges disagree until every one of them restarts", got, first)
	}
}
