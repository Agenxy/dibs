package mcp

import (
	"strings"
	"testing"
)

// A registration with no agent block must not take the daemon down.
//
// The agent block is optional and descriptive, so most registrations arrive
// without one. The first version of the hint read op.Agent.Harness
// unconditionally and panicked on a nil pointer inside an HTTP handler: every
// plain register would have killed the connection, and the feature that
// caused it is a nicety the caller never asked for.
func TestAHarnesslessRegistrationGetsNoHintAndDoesNotPanic(t *testing.T) {
	if hint := pluginHint("", false, false, true); hint != nil {
		t.Errorf("hint = %v, want nil for an unknown harness", hint)
	}
	if hint := pluginHint("emacs", false, false, true); hint != nil {
		t.Errorf("hint = %v, want nil: inventing a plugin for a harness we do not "+
			"support is worse than saying nothing", hint)
	}
}

// A session whose hooks already fired is told so, and given no homework.
//
// The daemon knows this before the agent's first turn, because SessionStart runs
// ahead of it. Asking an already-configured agent to go and verify something the
// server can already see is busywork on turn one, and busywork is how a
// first-connection nudge gets learned as noise and filtered out thereafter.
func TestAnAlreadyHookedSessionIsToldItIsDone(t *testing.T) {
	live := pluginHint("claude-code", false, true, true)
	if live == nil {
		t.Fatal("no hint for a fresh registration")
	}
	if live["hooks_live"] != true {
		t.Error("hooks_live is not reported as true")
	}
	if _, hasHomework := live["verify"]; hasHomework {
		t.Error("an agent whose hooks demonstrably work was still handed a verification " +
			"step; the server already had the answer")
	}
	dark := pluginHint("claude-code", false, false, true)
	if dark["hooks_live"] != false {
		t.Error("hooks_live is not reported as false")
	}
	if _, ok := dark["verify"]; !ok {
		t.Error("an agent with no hook traffic was given no way to check")
	}
	// The negative must stay an observation. "You have not installed it" would be
	// a conclusion the daemon cannot support: a plugin installed during THIS
	// session is genuinely present and genuinely inert until the next one.
	// Checked by what the sentence CLAIMS, not by which words appear in it. An
	// earlier version of this test searched for "missing" and failed on the very
	// hedge it was meant to require. "that is not proof the plugin is missing",
	// which is the same category of error as the code it guards: matching a token
	// instead of reading the assertion.
	status, _ := dark["status"].(string)
	if !strings.Contains(status, "not proof") {
		t.Errorf("status states absence as fact instead of reporting an observation: %q", status)
	}
	if !strings.Contains(status, "inert") {
		t.Errorf("status does not explain why a real install can still show no hook "+
			"traffic, which is the case an agent would otherwise misread: %q", status)
	}
}

// The nudge is for first contact only.
//
// A reattach is the same agent returning after losing context. Repeating an
// install prompt to somebody who already decided is how a hint that mattered
// once becomes noise that gets filtered out every time after.
func TestTheHintIsNotRepeatedOnReattach(t *testing.T) {
	if hint := pluginHint("claude-code", false, false, true); hint == nil {
		t.Fatal("a fresh claude-code registration got no hint: the one moment the " +
			"agent has just told us its harness is the one moment this is news")
	}
	if hint := pluginHint("claude-code", true, false, true); hint != nil {
		t.Errorf("hint = %v, want nil on reattach", hint)
	}
}

// The resource must be renderable, carry the files, and never claim the plugin
// is missing: the daemon cannot see that.
func TestThePluginDocIsServableAndHonest(t *testing.T) {
	doc := pluginDoc()
	if len(doc) < 200 {
		t.Fatalf("plugin doc is %d bytes: it should carry the files", len(doc))
	}
	for _, want := range []string{"hooks/hooks.json", "setup", "check", "claude-code"} {
		if !strings.Contains(doc, want) {
			t.Errorf("plugin doc does not mention %q", want)
		}
	}
	if strings.Contains(doc, "not installed") {
		t.Error("the doc claims to know whether the plugin is installed; the daemon " +
			"cannot see that, and asserting it would be a guess presented as a fact")
	}
}

// A harness whose delivery is not guaranteed is never told it can stop polling.
//
// Hook traffic and DELIVERY are different facts, and the hint once used one
// sentence for every harness: a Codex agent whose hooks had fired was told
// "mail will arrive, you do not need to poll" in the same result whose
// catalogue entry said pull-only. An agent that believes the first stops
// checking and silently loses mail. That is the guarantee here and it has not
// changed.
//
// The REASON changed, and this test used to freeze the old one. It said Codex
// fires hooks only as subprocesses, which Dibs refuses to be, so it could never
// wake an agent. That stopped being true on 2026-08-18: openai/codex#39296
// wired an MCP executor into every session and mcp_tool hooks now execute. So
// the assertion no longer demands the words "PULL-ONLY", which asserted a fact
// about the harness. It demands the INSTRUCTION, which is what protects the
// agent: keep checking in, whatever the harness turns out to support.
func TestAHarnessWithNoWakePathIsNotToldToStopPolling(t *testing.T) {
	codex := pluginHint("codex", false, true, true)
	if codex == nil {
		t.Fatal("no hint for a fresh codex registration")
	}
	note, _ := codex["note"].(string)
	if strings.Contains(note, "do not need to poll") {
		t.Errorf("codex was told it need not poll, but it has no wake path: %q", note)
	}
	if !strings.Contains(note, "check_in") {
		t.Errorf("codex was not told to keep checking in: on a harness whose delivery "+
			"depends on the build, the pull rhythm is the floor and dropping it is "+
			"how mail goes unread: %q", note)
	}

	// And the harness that DOES deliver still says so: the fix must not flatten
	// both into the same cautious sentence, which would waste the one thing
	// installing the plugin buys.
	cc := pluginHint("claude-code", false, true, true)
	ccNote, _ := cc["note"].(string)
	if !strings.Contains(ccNote, "do not need to poll") {
		t.Errorf("claude-code no longer advertises delivery: %q", ccNote)
	}
}

// An agent with no session id is told the truth about why nothing wakes it.
//
// The old text pointed at the plugin ("hooks are read at session start, so one
// installed during this session stays inert"), which sends the agent to check
// something that is very likely fine. A lifecycle hook resolves an agent by the
// session id the HARNESS knows: an agent that registered without one cannot be
// found by any hook, however perfectly the plugin is installed. Measured on a
// live board, where `dibs doctor` reported hooks resolving while that agent's
// mail sat unread.
func TestAnAgentWithNoSessionIdIsToldWhyNothingWakesIt(t *testing.T) {
	hint := pluginHint("claude-code", false, false, false)
	if hint == nil {
		t.Fatal("no hint at all for a fresh Claude Code registration")
	}
	status, _ := hint["status"].(string)
	if !strings.Contains(status, "session_id") {
		t.Errorf("status = %q: it does not name the actual cause", status)
	}
	// The agent can fix this one itself, so the fix has to be in the result.
	fix, _ := hint["fix"].(string)
	for _, want := range []string{"same nonce", "check_in", "await_events"} {
		if !strings.Contains(fix, want) {
			t.Errorf("fix = %q: missing %q", fix, want)
		}
	}
	// And it must not send the agent off to audit a plugin that is not the
	// problem: that is the wasted turn this replaces.
	if hint["verify"] != nil {
		t.Errorf("verify = %v: the plugin is not what is broken here", hint["verify"])
	}
}

// The install nudge is four sentences and it is worth reading exactly once.
//
// register has TWO continuity paths and only one of them was checked: an agent
// that is still active and registers again with its nonce comes back `resumed`,
// not `reattached`, so it was treated as a first connection every time and read
// the whole paragraph again on every register. An operator reported exactly
// that against v0.0.6. A hint that repeats gets trained away as noise, which
// costs the one registration where it is news.
func TestTheInstallNudgeIsNotRepeatedOnEveryRegister(t *testing.T) {
	srv, _ := newServer(t)
	const nonce = "b8c1f0a2d4e6a8c0b2d4f6a8c0e2d4f6"
	args := map[string]any{
		"name":    "returning",
		"nonce":   nonce,
		"harness": "claude-code",
		"cwd":     t.TempDir(),
	}

	first := toolCall(t, srv, "register", args)
	if _, ok := first["plugin"]; !ok {
		t.Fatal("no nudge on the FIRST registration: the probe is not measuring the hint at all")
	}

	// Same name, same nonce, agent still active: the resumed path.
	again := toolCall(t, srv, "register", args)
	if resumed, _ := again["resumed"].(bool); !resumed {
		t.Fatalf("second register did not resume, so this does not exercise the reported case: %v", again)
	}
	if hint, repeated := again["plugin"]; repeated {
		t.Errorf("install nudge repeated on a resumed registration: %v", hint)
	}
}
