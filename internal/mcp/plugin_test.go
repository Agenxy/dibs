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
	if hint := pluginHint("", false, false); hint != nil {
		t.Errorf("hint = %v, want nil for an unknown harness", hint)
	}
	if hint := pluginHint("emacs", false, false); hint != nil {
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
	live := pluginHint("claude-code", false, true)
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
	dark := pluginHint("claude-code", false, false)
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
	if hint := pluginHint("claude-code", false, false); hint == nil {
		t.Fatal("a fresh claude-code registration got no hint: the one moment the " +
			"agent has just told us its harness is the one moment this is news")
	}
	if hint := pluginHint("claude-code", true, false); hint != nil {
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

// A harness with hooks but no wake path is never told it can stop polling.
//
// Hook traffic and DELIVERY are different facts. Codex fires hooks as
// subprocesses, which Dibs refuses to be, so it can have live hooks and still
// have no way to wake an agent. The hint used one sentence for every harness, so
// a Codex agent whose hooks had fired was told "mail will arrive, you do not need
// to poll" in the same result whose catalogue entry said mail is pull-only. An
// agent that believed the first stops checking and silently loses mail.
func TestAHarnessWithNoWakePathIsNotToldToStopPolling(t *testing.T) {
	codex := pluginHint("codex", false, true)
	if codex == nil {
		t.Fatal("no hint for a fresh codex registration")
	}
	note, _ := codex["note"].(string)
	if strings.Contains(note, "do not need to poll") {
		t.Errorf("codex was told it need not poll, but it has no wake path: %q", note)
	}
	if !strings.Contains(note, "PULL-ONLY") {
		t.Errorf("codex was not told mail is pull-only there: %q", note)
	}

	// And the harness that DOES deliver still says so: the fix must not flatten
	// both into the same cautious sentence, which would waste the one thing
	// installing the plugin buys.
	cc := pluginHint("claude-code", false, true)
	ccNote, _ := cc["note"].(string)
	if !strings.Contains(ccNote, "do not need to poll") {
		t.Errorf("claude-code no longer advertises delivery: %q", ccNote)
	}
}
