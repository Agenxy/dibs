package mcp

import (
	"encoding/json"
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
func agentInfo(params json.RawMessage, a *toolArgs, session *clientInfoJSON) *core.AgentInfo {
	info := &core.AgentInfo{
		Model:    a.Model,
		Provider: a.Provider,
		Surface:  a.Surface,
		Effort:   a.Effort,
		Title:    a.Title,
		// Canonicalised on the way in, because this is not just a label: it is
		// what a lifecycle hook matches against when it has no session id to
		// resolve. Stored as given, an agent registered from /tmp/x could never
		// be found by a hook asking about /private/tmp/x, or vice versa.
		CWD:    canonPath(a.CWD),
		Branch: a.Branch,
		Host:   a.Host,
	}
	// Resolved HERE, once, at registration. This is the only impure step in
	// assembling an identity: paths.ProjectName shells out to Git on a cache
	// miss, bounded at a second. It is affordable because register happens
	// once per agent, and it must not move anywhere hotter. In particular it
	// cannot be derived when the board is read: that runs on the single-writer
	// loop, where a cold `git rev-parse` per agent would hold every other agent's
	// declare still. The engine already learned this once, which is why the
	// matcher's repo lens is resolved off the loop and handed in.
	info.Project = paths.ProjectName(info.CWD)
	// The identity behind the label. Resolved in the same breath because both
	// come from one memoised Identify, and recorded because the fold compares
	// them and the fold cannot call Git.
	info.RepoDir, info.RepoRemote, info.RepoRoots, _ = paths.Identify(info.CWD).Identity()
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
