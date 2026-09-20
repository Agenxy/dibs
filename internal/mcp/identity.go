package mcp

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/paths"
)

// agentInfo assembles who is behind an agent, for the human reading the board.
//
// Two sources, deliberately kept apart:
//   - harness + version come from the MCP handshake's clientInfo. The *client*
//     states these, not the model, so they are the trustworthy half.
//   - model, provider, surface, effort are self-reported. No harness puts the
//     model on the wire (verified against Claude Code, Codex, opencode, pi), so
//     asking the agent is the only honest route.
//
// None of it grants anything: a wrong value misleads a reader, it cannot
// escalate. That is why it is safe to accept self-reported fields at all.
// resolveLocation records where an agent is working and everything the server
// derives from that, as one group.
//
// The agent asserts the CWD and the SERVER derives the rest. That split is the
// whole reason mergeIdentity refuses project and the repo fields as settable:
// an agent asserting them could make its work look like it lives in a
// repository it has never touched. Deriving them here keeps that true while
// letting the agent correct the one field it does own.
//
// Canonicalised, because this is not just a label: it is what a lifecycle hook
// matches against when it has no session id to resolve. Stored as given, an
// agent registered from /tmp/x could never be found by a hook asking about
// /private/tmp/x, or vice versa.
//
// The only impure step in assembling an identity: paths.ProjectName shells out
// to Git on a cache miss, bounded at a second. Affordable because it runs at
// registration, and now on the rare update that carries a cwd, and it must not
// move anywhere hotter. In particular it cannot be derived when the board is
// READ: that runs on the single-writer loop, where a cold `git rev-parse` per
// agent would hold every other agent's declare still. The engine learned that
// once already, which is why the matcher's repo lens is resolved off the loop
// and handed in.
func resolveLocation(info *core.AgentInfo, cwd string) {
	info.CWD = canonPath(cwd)
	info.Project = paths.ProjectName(info.CWD)
	// The identity behind the label, resolved in the same breath because both
	// come from one memoised Identify, and recorded because the fold compares
	// them and the fold cannot call Git.
	id := paths.Identify(info.CWD)
	info.RepoDir, info.RepoRemote, info.RepoRoots, _ = id.Identity()
	// The checkout's own top level, which is what a path is measured FROM. The
	// three above say WHICH repository; this one is what lets the fold subtract a
	// worktree prefix and compare two claims on one tracked file. Same source,
	// same memoised Identify, same bargain: recorded because the fold cannot call
	// Git and must reach the same verdict on replay.
	info.RepoRoot = id.WorktreeID
}

// resolveLocationFor picks the resolver by where the caller is: this
// machine's Git for a local caller, the bridge's word for a remote one.
func resolveLocationFor(ctx context.Context, params json.RawMessage, info *core.AgentInfo, cwd string) {
	if remoteCaller(ctx, resolveHostID(ctx, params)) {
		resolveRemoteLocation(info, cwd, params)
		return
	}
	resolveLocation(info, cwd)
}

// resumeIdentity is what a resume records about where it happens: WHICH
// MACHINE, and nothing else. A resume is a new activation and it may be on
// another computer than the last: the same nonce presented from a laptop
// after registering on a desktop. It carried no identity at all, so the row
// kept the old host, its wakes went to the machine it had left and its new
// claims were keyed there. The host is what the connection established, as
// it is for register and update; nothing is read off the wire here. Nil
// when the connection established none. Round ten of the pre-release
// review.
func resumeIdentity(ctx context.Context, params json.RawMessage) *core.AgentInfo {
	host := resolveHostID(ctx, params)
	if host == "" {
		return nil
	}
	return &core.AgentInfo{HostID: host}
}

// resolveRemoteLocation is resolveLocation for a caller on ANOTHER machine.
//
// The path it sends names nothing on this filesystem, so canonicalising it
// here (symlinks, /private) and asking Git here both answer about the wrong
// computer: a remote-only checkout got no repository identity at all, so
// the portable repository rule, which exists for two clones on two machines,
// could not fire for the one case it is for. Worse was possible: a checkout
// of something ELSE at the same path on the hub would have lent its identity.
// The remote bridge resolves its own checkout and sends it (RepoMetaKey); the
// hub takes that word for a remote caller, which is the same trust as the
// host id it arrived with. Found by the pre-release review.
func resolveRemoteLocation(info *core.AgentInfo, cwd string, params json.RawMessage) {
	info.CWD = paths.Portable(cwd)
	r := metaRepo(params)
	info.RepoDir, info.RepoRemote, info.RepoRoots, info.RepoRoot = r.Dir, r.Remote, r.Roots, r.Root
	if r.Root != "" {
		info.Project = paths.PortableBase(r.Root)
	}
}

// repoMeta is what a bridge says about the checkout its caller works in.
type repoMeta struct {
	Dir    string `json:"dir"`
	Remote string `json:"remote"`
	Roots  string `json:"roots"`
	Root   string `json:"root"`
}

func metaRepo(params json.RawMessage) repoMeta {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	var r repoMeta
	if json.Unmarshal(params, &p) != nil {
		return r
	}
	raw, ok := p.Meta[RepoMetaKey]
	if !ok {
		return r
	}
	_ = json.Unmarshal(raw, &r)
	for _, f := range []*string{&r.Dir, &r.Remote, &r.Roots, &r.Root} {
		*f = strings.TrimSpace(*f)
	}
	return r
}

func agentInfo(ctx context.Context, params json.RawMessage, a *toolArgs, session *clientInfoJSON) *core.AgentInfo {
	info := &core.AgentInfo{
		// DERIVED, never taken from toolArgs. An agent choosing its own machine
		// id chooses which other agents its claims stop colliding with, and the
		// host rule REMOVES collisions, so a wrong value here hides a real one.
		// This is the same line mergeIdentity draws around the repository
		// fields: the agent asserts where it is working, the server says what
		// that means.
		HostID:   resolveHostID(ctx, params),
		Model:    a.Model,
		Provider: a.Provider,
		Surface:  a.Surface,
		Effort:   a.Effort,
		Title:    a.Title,
		// Canonicalised on the way in, because this is not just a label: it is
		// what a lifecycle hook matches against when it has no session id to
		// resolve. Stored as given, an agent registered from /tmp/x could never
		// be found by a hook asking about /private/tmp/x, or vice versa.
		Branch: a.Branch,
		Host:   a.Host,
	}
	resolveLocationFor(ctx, params, info, a.CWD)
	h, v := clientIdentity(params)
	if h == "" && session != nil {
		// Nothing on this request, but the session introduced itself at
		// initialize. Stateless HTTP carries clientInfo only on the handshake,
		// so without this every harness that skips the stdio bridge lands on the
		// board anonymous.
		h, v = session.Title, session.Version
		if h == "" {
			h = session.Name
		}
	}
	if h != "" && !genericClient(h) {
		info.Harness, info.Version = h, v
	} else {
		// The client either said nothing or announced its SDK rather than
		// itself. hermes is the live case: it uses the official Python SDK and
		// arrives as {"name":"mcp","version":"0.1.0"}, so its agent read
		// `harness: mcp`: useless on a mixed fleet, and it would collide with
		// every other Python-SDK client.
		//
		// Falling back to the agent's own word is strictly worse trust, and that
		// is acceptable here for the reason stated above: none of this grants
		// anything, a wrong value misleads a reader and cannot escalate. A
		// self-reported "hermes" beats a correct-but-meaningless "mcp".
		//
		// Deriving it from the parent process was tried and removed: harnesses
		// wrap the bridge. hermes spawns it under tools/mcp_stdio_watchdog.py and
		// Claude Desktop under a `disclaimer` helper, so the parent is never the
		// harness: the heuristic produced "python" and "disclaimer".
		if a.Harness != "" {
			info.Harness = a.Harness
		}
		if v := clientVersion(params); v != "" && info.Harness != "" {
			info.Version = v // the SDK version is still the truthful one
		}
	}
	if *info == (core.AgentInfo{}) {
		return nil // nothing worth showing; keep the agent clean
	}
	return info
}

// selfReported is the half of an identity an agent may revise about itself.
//
// Deliberately not agentInfo(): that one resolves the project and the repo
// identity from the filesystem, which is a Git call, and it reads clientInfo
// from the handshake. Neither belongs on `update`. The repo half is resolved by
// the server and compared by the fold, so an agent must not be able to assert
// it; harness and version are the client's word rather than the model's, which
// is the one part of the board that is not self-description; and the Git call
// is affordable once per agent at registration, not on every revision.
func selfReported(a *toolArgs) *core.AgentInfo {
	info := &core.AgentInfo{
		Model:    a.Model,
		Provider: a.Provider,
		Effort:   a.Effort,
		Surface:  a.Surface,
		Title:    a.Title,
		Branch:   a.Branch,
	}
	if *info == (core.AgentInfo{}) {
		return nil
	}
	return info
}

// clientIdentity pulls the human-facing harness name and version out of either
// the 2026 per-request _meta clientInfo or a legacy initialize's clientInfo.
// Prefers `title` ("Claude Code") over `name` ("claude-code"): the board is
// read by a person.
func clientIdentity(params json.RawMessage) (harness, version string) {
	var p struct {
		ClientInfo *clientInfoJSON `json:"clientInfo"`
		Meta       struct {
			ClientInfo *clientInfoJSON `json:"io.modelcontextprotocol/clientInfo"`
		} `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return "", ""
	}
	ci := p.ClientInfo
	if ci == nil {
		ci = p.Meta.ClientInfo
	}
	if ci == nil {
		return "", ""
	}
	if ci.Title != "" {
		return ci.Title, ci.Version
	}
	return ci.Name, ci.Version
}

// genericClient reports whether a declared client name is an SDK placeholder
// rather than a product. Such a name identifies the library, not the agent.
func genericClient(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "mcp", "client", "unknown", "mcp-client", "mcpclient", "python-sdk", "typescript-sdk":
		return true
	}
	return false
}

// clientVersion is the version half of clientIdentity, needed on its own when
// the name is discarded as generic but the version is still worth keeping.
func clientVersion(params json.RawMessage) string {
	_, v := clientIdentity(params)
	return v
}

type clientInfoJSON struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

var forcePanel = os.Getenv("DIBS_FORCE_PANEL") == "1"

// clientWantsUI reports whether the caller declared the MCP Apps extension.
// The stdio bridge forwards the capability from the initialize handshake, since
// the HTTP hop is stateless and cannot remember it on its own. Absent that
// signal we assume no renderer, which is the safe default: a panel payload sent
// to a client that cannot draw is context the model pays for twice.
func clientWantsUI(params json.RawMessage) bool {
	// Escape hatch for measuring a host directly. The capability signal reaches
	// us through the stdio bridge, so a bridge process older than that code
	// cannot forward it, and respawning the bridge means restarting the host,
	// which is exactly what you are trying to avoid when you want to test the
	// host you are sitting in. DIBS_FORCE_PANEL=1 sends the panel to everyone.
	if forcePanel {
		return true
	}
	var p struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	v, _ := p.Meta["com.dibs/ui"].(bool)
	return v
}

// remoteCaller reports whether a caller resolved to a machine other than this
// daemon's. Unknown on either side is not remote: the rules that key on this
// REMOVE collisions and lend trust, and "no idea" must not do either.
func remoteCaller(ctx context.Context, hostID string) bool {
	own, _ := ctx.Value(ownHostKey{}).(string)
	return hostID != "" && own != "" && hostID != own
}

// ownHostKey carries this daemon's own host id on every request, so the
// tool path can tell a remote caller from a local one without reaching for
// the engine.
type ownHostKey struct{}

// hostIDKey carries the machine identity the transport established for this
// request. A context value rather than a parameter because it is a fact about
// the CONNECTION, and the tool-call path deliberately knows nothing about HTTP:
// the same reason clientInfo is read once at the edge and carried forward.
type hostIDKey struct{}

// withTransportHost stamps a request context with what the transport can prove
// about where the caller is.
//
// local is true when the request arrived over loopback, which nothing off this
// machine can reach, so the daemon's own node id is EVIDENCE for that caller
// rather than a claim. A remote caller gets the empty string here and falls
// back to what its own bridge asserts, which is weaker and is documented as
// weaker.
func withTransportHost(ctx context.Context, local bool, node string) context.Context {
	if node != "" {
		ctx = context.WithValue(ctx, ownHostKey{}, node)
	}
	if !local || node == "" {
		return ctx
	}
	return context.WithValue(ctx, hostIDKey{}, node)
}

// resolveHostID answers which computer this caller is on.
//
// A remote caller's bridge is the only thing that knows, so its word is
// taken and marked as its word. A loopback caller that asserts nothing is
// stamped with this daemon's own identity: nothing off this machine reaches
// loopback, so that much is evidence.
//
// A loopback caller that asserts a DIFFERENT machine used to be overruled,
// on the grounds that it cannot be anywhere but here. It can: the documented
// transport for a machine without Supgang is `ssh -L`, and every call it
// forwards arrives from 127.0.0.1 carrying the remote bridge's host id. The
// overrule stamped those agents as the hub's own, so their wakes were run
// here instead of through their host bridge and their paths were compared
// as if on one filesystem. Found by the pre-release review.
//
// What the change gives up is small and already given up elsewhere: a
// bridge on this machine started with a foreign DIBS_HOST_ID can excuse its
// agents from path collisions here. That bridge holds the bearer secret,
// which is the whole board; docs/NETWORK.md §2 states host identity is
// asserted, as strong as the secret and no stronger, until §6 proves it.
func resolveHostID(ctx context.Context, params json.RawMessage) string {
	if asserted := metaHost(params); asserted != "" {
		return asserted
	}
	if v, ok := ctx.Value(hostIDKey{}).(string); ok && v != "" {
		return v
	}
	return ""
}

// isLoopback reports whether an address is one nothing off this machine can
// reach. A malformed or absent address is NOT loopback: the conservative
// reading, since the whole point of the answer is that it is evidence.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// metaHost reads the machine id a remote bridge attaches to its calls.
func metaHost(params json.RawMessage) string {
	var p struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	v, _ := p.Meta[HostMetaKey].(string)
	return strings.TrimSpace(v)
}

// metaSession reads the harness session id the stdio bridge attaches to every
// tool call. Empty when the caller is not behind the bridge.
func metaSession(params json.RawMessage) string {
	var p struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	v, _ := p.Meta[SessionMetaKey].(string)
	return v
}

// handshakeClient pulls clientInfo out of an initialize/server-discover so the
// session store can remember it for the stateless calls that follow.
func handshakeClient(params json.RawMessage) *clientInfoJSON {
	var p struct {
		ClientInfo *clientInfoJSON `json:"clientInfo"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil
	}
	return p.ClientInfo
}
