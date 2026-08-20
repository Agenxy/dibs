package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// runningEngine is a loop the wiring tests can actually call: e.query() sends
// on e.ops, which is nil on a zero-value Engine, so an exported method called
// on one blocks forever rather than failing.
func runningEngine(t *testing.T) (*Engine, context.Context, context.CancelFunc) {
	t.Helper()
	e := New(core.NewState("test", core.DefaultLimits()), &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	return e, ctx, cancel
}

// A session announced by a harness hook is matched to the agent in that
// directory.
//
// The live failure: Codex's bridge registers the agent under `host-<ppid>`,
// the only name a spawned process can observe, while Codex's own hooks send
// the uuid Codex calls the session. Both are right, neither matches, so
// hook_poll found no agent and the digest was empty. Mail sat unread while
// every call in the chain returned success.
func TestAHarnessSessionFindsTheAgentItIsRunning(t *testing.T) {
	now := time.Now()
	st := core.NewState("test", core.DefaultLimits())
	children := map[string]Child{
		// What SessionStart announced, before the model had done anything.
		"codex-abc": {SessionID: "codex-abc", CWD: "/repo/app", Seen: now},
	}

	got := announcedSession(children, st, "/repo/app/", now)
	if got != "codex-abc" {
		t.Fatalf("a session that announced itself from this directory was not "+
			"adopted (%q): the harness's hooks cannot find this agent, and its "+
			"mail is delivered to nobody", got)
	}
}

// The join refuses everything it cannot be sure of, and each refusal is a
// disclosure this path has already caused once.
//
// hook_poll carries no token by design, so whatever session id an agent holds
// is the key to its mail digest. Handing one out on a guess is how an
// unregistered session was previously given another agent's private mail
// including the body. The join therefore declines rather than guesses.
func TestTheJoinRefusesToGuess(t *testing.T) {
	now := time.Now()
	st := core.NewState("test", core.DefaultLimits())

	t.Run("a stranger cannot plant a session at a directory it does not own", func(t *testing.T) {
		// The real harness announced, and so did somebody else. Two candidates
		// for one directory is also the normal state of a repo with two agents
		// in it, and both are answered the same way.
		children := map[string]Child{
			"codex-abc": {SessionID: "codex-abc", CWD: "/repo/app", Seen: now},
			"planted":   {SessionID: "planted", CWD: "/repo/app", Seen: now},
		}
		if got := announcedSession(children, st, "/repo/app", now); got != "" {
			t.Errorf("adopted %q with two sessions claiming one directory: a planted "+
				"announcement would take delivery of the next agent's mail", got)
		}
	})

	t.Run("a session another agent already holds is never taken", func(t *testing.T) {
		held := core.NewState("test", core.DefaultLimits())
		r, _, err := held.Apply(&core.Op{
			Kind: core.OpRegister, Name: "incumbent", AgentKind: core.KindPersistent,
			Nonce: "n", SessionID: "codex-abc",
			Agent: &core.AgentInfo{CWD: "/repo/app"},
		}, now)
		if err != nil || r["agent_id"] == nil {
			t.Fatalf("setup: the incumbent did not register: %v %v", r, err)
		}
		children := map[string]Child{
			"codex-abc": {SessionID: "codex-abc", CWD: "/repo/app", Seen: now},
		}
		if got := announcedSession(children, held, "/repo/app", now); got != "" {
			t.Errorf("adopted %q, which another agent holds: its wake digest would "+
				"be delivered to whoever registered second", got)
		}
	})

	t.Run("a stale announcement expires", func(t *testing.T) {
		children := map[string]Child{
			"yesterday": {SessionID: "yesterday", CWD: "/repo/app", Seen: now.Add(-2 * time.Hour)},
		}
		if got := announcedSession(children, st, "/repo/app", now); got != "" {
			t.Errorf("adopted %q from a dead session: today's agent would answer to "+
				"yesterday's hooks", got)
		}
	})

	t.Run("a different directory is a different agent", func(t *testing.T) {
		children := map[string]Child{
			"codex-abc": {SessionID: "codex-abc", CWD: "/repo/other", Seen: now},
		}
		if got := announcedSession(children, st, "/repo/app", now); got != "" {
			t.Errorf("adopted %q announced from somewhere else entirely", got)
		}
	})

	t.Run("no directory means no match", func(t *testing.T) {
		children := map[string]Child{
			"codex-abc": {SessionID: "codex-abc", CWD: "", Seen: now},
		}
		if got := announcedSession(children, st, "", now); got != "" {
			t.Errorf("matched %q on an empty directory, which every unknown cwd "+
				"would then match", got)
		}
	})
}

// The whole path: an agent registered under one name for its session, woken by
// a hook that knows it by another.
//
// The unit test above covers the decision; this covers the wiring, because the
// decision being right has never been what broke here. Both times this area
// failed, the logic was correct and nothing reached it. The first version of
// this fix passed its own unit tests and left this probe failing, because it
// assumed the bridge sent no session id when in truth it sends a different one.
func TestMailReachesAnAgentRegisteredByABlindBridge(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	// SessionStart: the harness reports itself. No agent exists yet.
	if _, err := e.NoteChildSession(ctx, Child{
		SessionID: "codex-abc", CWD: "/repo/app", State: "running",
	}); err != nil {
		t.Fatal("setup: the session start hook failed:", err)
	}

	// The bridge registers under the name IT can observe: the pid of the
	// process that spawned it. That is a different name for the same session,
	// and it is the whole problem, not a missing value.
	r, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "codex-worker", AgentKind: core.KindPersistent,
		Nonce: "n-c", SessionID: "host-10602",
		Agent: &core.AgentInfo{CWD: "/repo/app"},
	})
	if err != nil {
		t.Fatal("setup: register failed:", err)
	}
	tok, _ := r["token"].(string)
	if tok == "" {
		t.Fatal("setup: no token")
	}

	// Somebody writes to it.
	sender, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "peer", AgentKind: core.KindPersistent, Nonce: "n-p",
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: sender["token"].(string), To: "codex-worker",
		MsgType: core.MsgQuestion, Body: "are you there?",
	}); err != nil {
		t.Fatal("setup: the message was not sent:", err)
	}

	// Stop fires with the id the HARNESS knows, which is not the id the bridge
	// registered under.
	out, err := e.HookPoll(ctx, "codex-abc", "Stop", "/repo/app", false)
	if err != nil {
		t.Fatal(err)
	}
	// Nested, not top level: the digest the model sees is under
	// hookSpecificOutput, and a probe reading the wrong key would pass this
	// test against the very bug it was written for.
	spec, _ := out["hookSpecificOutput"].(map[string]any)
	ctxText, _ := spec["additionalContext"].(string)
	if ctxText == "" {
		t.Fatal("the harness's own Stop hook found nothing waiting. This is the " +
			"reported failure: the agent is on the board under the bridge's name for " +
			"its session, the hook asks under the harness's, and nothing reconciles " +
			"them, so no hook can ever reach it")
	}
}

// An agent already on the board when this shipped gets its session at check_in.
//
// It is the only call some agents ever make again: nothing obliges a running
// agent to register or update a second time, so without this every agent that
// existed before the join stays permanently unreachable by its own harness's
// hooks, which is most of the board on the machine this was written for.
func TestAnAgentAlreadyOnTheBoardBindsAtCheckIn(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	// Registered BEFORE any announcement, exactly like an agent that predates
	// the join: no session id anywhere.
	r, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "legacy", AgentKind: core.KindPersistent,
		Nonce: "n-legacy", SessionID: "host-999",
		Agent: &core.AgentInfo{CWD: "/repo/app"},
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	tok, _ := r["token"].(string)
	if sid, _ := r["session_id"].(string); sid != "host-999" || tok == "" {
		t.Fatalf("setup: expected the bridge's own session name and a token, got %q / %q", sid, tok)
	}

	// Its harness restarts and announces itself.
	if _, err := e.NoteChildSession(ctx, Child{
		SessionID: "codex-later", CWD: "/repo/app", State: "running",
	}); err != nil {
		t.Fatal("setup:", err)
	}

	got, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	if sid, _ := got["session_id"].(string); sid != "codex-later" {
		t.Fatalf("check_in did not bind the announced session (%q): an agent that "+
			"never registers again is never reachable by its hooks", sid)
	}
}

// The human's mailbox is not adoptable by a harness session.
//
// It holds what people asked the operator directly, and a Codex session in the
// same directory picking it up would take delivery of that. Two things prevent
// it today, the row registering WITH a session id and carrying no cwd, and
// both are incidental to code with other purposes. This states it as the rule
// so that changing either is a failing test rather than a quiet disclosure.
func TestTheHumansRowDoesNotAdoptAHarnessSession(t *testing.T) {
	now := time.Now()
	st := core.NewState("test", core.DefaultLimits())
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "human", AgentKind: core.KindPersistent,
		Nonce: "h", SessionID: "human-nonce",
		Agent: &core.AgentInfo{Harness: "dibs web", Surface: "web"},
	}, now); err != nil {
		t.Fatal("setup:", err)
	}
	children := map[string]Child{
		"codex-abc": {SessionID: "codex-abc", CWD: "/repo/app", Seen: now},
	}
	// The row has no cwd at all, so there is nothing for an announcement to
	// match: an empty cwd must never be a wildcard.
	if got := announcedSession(children, st, "", now); got != "" {
		t.Errorf("the human's row adopted %q: a harness session in that directory "+
			"would then be handed what people asked the operator", got)
	}
}

// A session id is never invented for an agent that did not announce one, even
// when it is the only agent in the directory.
//
// The difference from the fallback core.AgentForHook refuses: there, an
// unknown session asked to be resolved to whatever agent shared its directory.
// Here the harness itself announced the id through its own hook before any
// agent existed. Remove the announcement and the join must find nothing.
func TestNoAnnouncementMeansNoSession(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	r, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "lonely", AgentKind: core.KindPersistent,
		Nonce: "n-l", Agent: &core.AgentInfo{CWD: "/repo/app"},
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if sid, _ := r["session_id"].(string); sid != "" {
		t.Errorf("an agent that announced nothing was given session %q", sid)
	}
	if _, err := e.HookPoll(ctx, "some-strangers-session", "Stop", "/repo/app", false); err != nil {
		t.Fatal(err)
	}
}
