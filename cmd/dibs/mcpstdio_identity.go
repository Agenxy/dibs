package main

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// enrichRegister fills in who the agent is, using the environment the harness
// gave us. The bridge is spawned BY the harness, so it inherits that
// environment: the agent itself never has to know or say any of this.
//
// The list is a strict allowlist, and it always will be. The same environment
// holds CODEX_API_KEY, OPENCODE_API_KEY, CODEX_ACCESS_TOKEN and friends; a
// prefix scan or a "copy everything interesting" heuristic would put
// credentials on a shared board. Only these keys, only these meanings.
//
// Anything the agent passed explicitly wins: it knows things the environment
// cannot, above all which model it is.
// Each entry names the harness that owns the variable. That gate is not
// decoration: environment is inherited transitively, so an opencode agent
// launched from a shell inside Claude Code sees CLAUDE_PID and
// CLAUDE_CODE_ENTRYPOINT, and would be labelled "claude-desktop": mislabelling
// agents in exactly the mixed fleet this feature exists to clarify. A variable
// is trusted only when the connected client says it owns it.
var identityEnv = []struct{ harness, env, field string }{
	{"claude", "CLAUDE_CODE_ENTRYPOINT", "surface"}, // "claude-desktop" vs "cli"
	{"claude", "CLAUDE_CODE_SESSION_ID", "session_id"},
	{"claude", "CLAUDE_EFFORT", "effort"},
	{"opencode", "OPENCODE_CALLER", "surface"},
	{"pi", "PI_MODEL", "model"}, // pi is the one harness that publishes the model
	{"pi", "PI_PROVIDER", "provider"},
}

// clientIs reports whether the connected client is the named harness, per the
// name it gave at initialize. Unknown client ⇒ trust nothing harness-specific.
func clientIs(harness string) bool {
	if lastClientInfo == nil {
		return false
	}
	name, _ := lastClientInfo["name"].(string)
	title, _ := lastClientInfo["title"].(string)
	return strings.Contains(strings.ToLower(name+" "+title), harness)
}

// lastClientInfo is captured from the initialize handshake. The HTTP hop is
// stateless, so without this the harness identity the client already told us
// would never reach register.
var lastClientInfo map[string]any

// lastWantsUI records whether the client declared the MCP Apps extension at
// initialize. The HTTP hop is stateless, so the server cannot know this on its
// own: without forwarding it, every client looks like it cannot render.
var lastWantsUI bool

func noteClientInfo(msg map[string]any) {
	params, _ := msg["params"].(map[string]any)
	if params == nil {
		return
	}
	if ci, ok := params["clientInfo"].(map[string]any); ok {
		lastClientInfo = ci
	}
	if caps, ok := params["capabilities"].(map[string]any); ok {
		if ext, ok := caps["extensions"].(map[string]any); ok {
			_, lastWantsUI = ext["io.modelcontextprotocol/ui"]
		}
	}
}

func enrichRegister(line []byte) []byte {
	var msg map[string]any
	if json.Unmarshal(line, &msg) != nil {
		return line
	}
	if msg["method"] == "initialize" {
		noteClientInfo(msg)
		return line
	}
	// Every tools/call carries the renderer signal and this session's id, so
	// the server can decide per call whether a panel payload is worth sending,
	// and can attach an agent to the session it is actually running in.
	//
	// The session id used to ride on `register` alone, which is the one call an
	// agent might not make through this bridge. Register out of band, or with a
	// harness that was configured before the bridge existed, and the agent is
	// on the board with no session for the rest of its life: every lifecycle
	// hook then quotes a session id that matches nothing, AgentForHook returns
	// nil, and mail is never pushed into that session. Measured on this
	// machine, where 9 wake polls in a row resolved to no agent while the board
	// held unread mail for the agent sitting in that very directory.
	//
	// Sending it on every call is what makes that self-healing: the first
	// authenticated call the agent makes through this bridge is enough.
	if msg["method"] == "tools/call" {
		if params, ok := msg["params"].(map[string]any); ok {
			meta, _ := params["_meta"].(map[string]any)
			if meta == nil {
				meta = map[string]any{}
				params["_meta"] = meta
			}
			if lastWantsUI {
				meta["com.dibs/ui"] = true
			}
			if sid := sessionID(); sid != "" {
				meta["com.dibs/session"] = sid
			}
			if out, err := json.Marshal(msg); err == nil {
				line = out
			}
		}
	}
	params, _ := msg["params"].(map[string]any)
	if params == nil || params["name"] != "register" {
		return line
	}
	if lastClientInfo != nil {
		params["clientInfo"] = lastClientInfo
	}
	args, _ := params["arguments"].(map[string]any)
	if args == nil {
		args = map[string]any{}
		params["arguments"] = args
	}
	var touched bool
	// Host, cwd and branch are observed for every harness; session id, title and
	// surface come from Claude Code's on-disk sidecar and so are Claude-only.
	// Anything the caller already filled in wins: we only supply what is blank.
	for k, v := range sessionContext(clientIs("claude")) {
		if cur, ok := args[k].(string); ok && cur != "" {
			continue
		}
		args[k] = v
		touched = true
	}
	for _, e := range identityEnv {
		if !clientIs(e.harness) {
			continue
		}
		v := os.Getenv(e.env)
		if v == "" {
			continue
		}
		if cur, ok := args[e.field].(string); ok && cur != "" {
			continue // the agent already said; it knows better
		}
		args[e.field] = v
		touched = true
	}
	// The bridge's own pid is the session's liveness, so the agent never has to
	// find one.
	//
	// pid drives the sweep's dead-agent detection (kill(pid,0) plus start-time).
	// Left to the model it is either absent. `"pid": 0`, which suppresses the
	// proc_alive signal entirely, or wrong: a live glm-4.6 run sent the literal
	// string "$$", failed, then shelled out to `echo $$` to recover.
	//
	// This process is the better answer regardless. It is spawned when the
	// session starts and exits when the session ends, so "is this pid alive" and
	// "is this agent still connected" are the same question. The harness's own
	// pid is not reachable: harnesses wrap the bridge, so the parent is a
	// watchdog or a launcher, not the agent.
	if cur, ok := args["pid"].(float64); !ok || cur == 0 {
		args["pid"] = os.Getpid()
		touched = true
	}
	// The NONCE, which is the credential the agent cannot be relied on to keep.
	//
	// See mcpstdio_nonce.go for why this belongs to the bridge. Briefly: the
	// nonce exists to survive a context ending, and it is the agent's context
	// that holds it, so it does not. The bridge is the only party here with a
	// memory that spans sessions.
	//
	// Same rule as every other field above: what the agent supplied wins, and
	// this only fills a blank. A supplied nonce is remembered too, so an agent
	// that manages its own credential once does not have to manage it twice.
	if _, had := args["nonce"]; !had {
		enrichNonce(args, pinnedNonce())
		if _, now := args["nonce"]; now {
			touched = true
		}
	} else {
		// Remembers what the agent supplied, so it need not manage it twice.
		enrichNonce(args, pinnedNonce())
	}
	// DIBS_HARNESS names the harness when its MCP client will not.
	//
	// Most clients identify themselves at initialize and this never fires. Some
	// announce their SDK instead: hermes uses the official Python SDK and arrives
	// as {"name":"mcp","version":"0.1.0"}, which reads as `harness: mcp` on the
	// board: useless on a mixed fleet, and identical for every Python-SDK
	// client.
	//
	// This is set in the harness's own MCP server config (hermes, codex and
	// opencode all support an `env` block there), so it is stated once by the
	// person wiring Dibs up, not guessed per call and never asked of the model.
	// Deriving it from the parent process was tried and removed: harnesses wrap
	// the bridge: hermes under tools/mcp_stdio_watchdog.py, Claude Desktop under
	// a `disclaimer` helper, so the parent is never the harness.
	if h := strings.TrimSpace(os.Getenv("DIBS_HARNESS")); h != "" {
		if cur, ok := args["harness"].(string); !ok || cur == "" {
			args["harness"] = h
			touched = true
		}
	}
	// Last resort for session_id, and the thing that makes reattach work at
	// all outside Claude Code.
	//
	// Reattach keys on (name, session_id). session_id is an ARGUMENT, so it
	// only gets set if the model types it, and models do not: a live opencode
	// run sent `"session_id":""` every time, so three consecutive runs of the
	// same agent produced oc-alpha, oc-alpha-2 and oc-alpha-3. A question sent
	// to the second was invisible to the third: the agent's own address changed
	// underneath it, silently.
	//
	// The bridge process is the right thing to key on. Harnesses spawn one
	// stdio bridge per session and hold it for that session's lifetime,
	// opencode's MCP.connectLocal passes no session identifier of its own, just
	// process.env plus user config, so there is nothing else to observe. THIS
	// PROCESS is the session: re-registering inside it reattaches, while a
	// genuinely new session gets a new bridge and correctly gets a new agent.
	if cur, ok := args["session_id"].(string); !ok || cur == "" {
		args["session_id"] = bridgeSessionID()
		touched = true
	}
	if !touched && lastClientInfo == nil {
		return line
	}
	out, err := json.Marshal(msg)
	if err != nil {
		return line // never drop a request because enrichment failed
	}
	return out
}

// sessionID is the harness session this bridge is running inside, resolved once.
//
// Memoised because it is now sent on every tool call and resolving it is file
// work: Claude Code's per-process sidecar, plus a bounded scan of the
// transcript for the session title. Once per bridge process is the right
// frequency for something that cannot change while the process lives.
var sessionOnce struct {
	sync.Once
	id string
}

func sessionID() string {
	sessionOnce.Do(func() {
		if v := sessionContext(clientIs("claude"))["session_id"]; v != "" {
			sessionOnce.id = v
			return
		}
		sessionOnce.id = bridgeSessionID()
	})
	return sessionOnce.id
}
