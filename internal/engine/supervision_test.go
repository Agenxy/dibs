package engine

import (
	"testing"
	"time"
)

// A later lifecycle event carries less than the first one did.
//
// codex's SessionStart hands over transcript_path; its Stop does not. Replacing
// the record on every event would discard the transcript exactly when
// supervision needs it most: at the end, when the question is whether the
// child finished or died.
func TestALaterEventDoesNotEraseWhatTheFirstOneCarried(t *testing.T) {
	e, now := &Engine{}, time.Now()

	e.noteChild(Child{
		SessionID: "s1", Transcript: "/tmp/rollout.jsonl", Model: "gpt-5.6", CWD: "/repo",
		State: StateForEvent("SessionStart"),
	}, now)
	// Stop carries the session and nothing else.
	e.noteChild(Child{SessionID: "s1", State: StateForEvent("Stop")}, now)
	got := e.children["s1"]
	if got.Transcript != "/tmp/rollout.jsonl" {
		t.Errorf("the transcript was lost on Stop (%q): it is the one field that makes\n"+
			"  a finished child distinguishable from a dead one", got.Transcript)
	}
	if got.Model != "gpt-5.6" || got.CWD != "/repo" {
		t.Errorf("other fields lost too: %+v", got)
	}
	if got.State != "finished" {
		t.Errorf("state = %q, want finished", got.State)
	}
}

// Blocked-on-permission is the state process forensics cannot see. From
// outside, a child waiting for a human and a child hung on a socket are
// identical; they need opposite responses, and only the harness knows which.
func TestBlockedIsRecordedWithWhatItIsWaitingFor(t *testing.T) {
	e, now := &Engine{}, time.Now()
	e.noteChild(Child{SessionID: "s2", State: "blocked", Blocked: "shell"}, now)
	got := e.children["s2"]
	if got.State != "blocked" || got.Blocked != "shell" {
		t.Errorf("got %+v, want blocked on shell", got)
	}
	if got.Since.IsZero() {
		t.Error("no timestamp: how long it has been blocked is the whole question")
	}
}

// An unrecognised event must not silently reset a child's state. Harnesses add
// events; a new one arriving should leave what is known alone rather than
// quietly marking a blocked agent as running again.
func TestAnUnknownEventLeavesTheStateAlone(t *testing.T) {
	if s := StateForEvent("SomethingCodexAddedLastWeek"); s != "" {
		t.Errorf("unknown event mapped to %q; it must map to nothing", s)
	}
	e, now := &Engine{}, time.Now()
	e.noteChild(Child{SessionID: "s3", State: "blocked", Blocked: "shell"}, now)
	e.noteChild(Child{SessionID: "s3", State: StateForEvent("PostCompact")}, now)
	if got := e.children["s3"]; got.State != "blocked" {
		t.Errorf("state = %q; an unrecognised event reset a blocked child to running", got.State)
	}
}

// A hook that errors on every turn of every unrelated session is a hook a
// person deletes. Most sessions on a machine have no agent.
func TestAnUnmatchedSessionIsNotAnError(t *testing.T) {
	e, now := &Engine{}, time.Now()
	if r := e.noteChild(Child{SessionID: "nobody", State: "running"}, now); r["ok"] != true {
		t.Errorf("an unmatched session was rejected: %v", r)
	}
	// But a call with nothing to record it against says so rather than
	// silently storing a nameless entry.
	r := e.noteChild(Child{State: "running"}, now)
	if r["ok"] != false {
		t.Errorf("a session-less report was accepted: %v", r)
	}
}

// A child that counts its own turns is believed when there is no transcript.
//
// opencode is the one harness whose progress cannot be observed from outside:
// sessions live in SQLite, where byte growth measures WAL churn, and its only
// append-only file is a single log SHARED by every run on the machine. Watching
// that would make every opencode agent look busy whenever any one of them was.
//
// So the child counts for itself and Dibs uses the counter. Without it, an
// opencode child is judged on CPU alone, which catches a hard stall and misses
// a slow one, the exact distinction this whole layer exists to make.
func TestAChildsOwnProgressCounterIsUsedAndIsMonotonic(t *testing.T) {
	e, now := &Engine{}, time.Now()

	e.noteChild(Child{SessionID: "oc", Progress: 3, State: "running"}, now)
	if got := e.children["oc"].Progress; got != 3 {
		t.Fatalf("progress = %d, want 3", got)
	}

	// A later event that says nothing about progress must not reset it. Most
	// lifecycle events carry no counter, and treating their silence as zero
	// would make a working child look frozen at every turn boundary.
	e.noteChild(Child{SessionID: "oc", State: StateForEvent("Stop")}, now)
	if got := e.children["oc"].Progress; got != 3 {
		t.Errorf("progress reset to %d by an event that did not mention it", got)
	}

	// It only goes up. A counter that went backwards is a restarted process
	// reusing a session, not work that un-happened, and treating it as a
	// decrease would read as a stall to the classifier.
	e.noteChild(Child{SessionID: "oc", Progress: 1, State: "running"}, now)
	if got := e.children["oc"].Progress; got != 3 {
		t.Errorf("progress went backwards to %d; the contract is monotonic", got)
	}

	e.noteChild(Child{SessionID: "oc", Progress: 9, State: "running"}, now)
	if got := e.children["oc"].Progress; got != 9 {
		t.Errorf("progress = %d, want 9: a real advance was dropped", got)
	}
}

// The field the published verification tells an operator to compare must not
// move on its own.
//
// The Codex plugin's verify step says to call spawned_agents before and after a
// turn boundary and compare. It used to say to compare "the entry", and
// spawned_agents computes since_seconds and seen_seconds with time.Since on
// every read: the entry changes because time passed, so an operator whose Stop
// hook never reached the daemon could follow the procedure exactly and be told
// delivery works. A verification step that cannot fail is worse than none,
// because it is the thing somebody runs when they already suspect a problem.
//
// So: `state` is stable across reads with no lifecycle event, and a Stop moves
// it. Both halves, because a field that never changes would also be "stable".
//
// WHAT THIS DOES NOT COVER, said plainly because the omission was read as
// coverage. Both halves stop short of production: the stability check reads
// through childrenSnapshot below rather than through Children, and the "a Stop
// moves it" half asks StateForEvent, which is a pure mapper, rather than
// sending an event through HookPoll. Cut announceHookSession out of HookPoll
// and every assertion here still passes while the published procedure this
// guard exists for silently stops working. That was verified by doing it.
//
// TestALifecycleEventReachesThePublishedChildState covers the wiring end to
// end; this one covers the two decisions underneath it. Keep both: the
// end-to-end test says the chain is connected, and these say the pieces are
// individually right, which is what tells you WHICH link broke.
func TestTheStateFieldIsWhatALifecycleEventMoves(t *testing.T) {
	e := &Engine{children: map[string]Child{
		"s1": {SessionID: "s1", State: "running", Since: time.Now(), Seen: time.Now()},
	}}

	// Two reads, no event between them.
	first := e.childrenSnapshot()
	second := e.childrenSnapshot()
	if first["s1"]["state"] != second["s1"]["state"] {
		t.Errorf("`state` changed between two reads with no lifecycle event: %v then %v. "+
			"The published verification compares it across a turn boundary, and a "+
			"field that moves on its own makes that check pass for a broken hook",
			first["s1"]["state"], second["s1"]["state"])
	}
	// The elapsed fields DO move, which is why the instruction must not name
	// them. If this ever stops being true the instruction can be simplified.
	if first["s1"]["since_seconds"] == second["s1"]["since_seconds"] {
		t.Log("since_seconds did not move between reads; the instruction's caution " +
			"about elapsed fields may no longer be needed")
	}

	// And a Stop moves `state`, or comparing it proves nothing either.
	if got := StateForEvent("Stop"); got != "finished" {
		t.Fatalf("Stop maps to %q, not \"finished\": the published verification tells "+
			"an operator to look for `finished` after the boundary", got)
	}
	if StateForEvent("SessionStart") == StateForEvent("Stop") {
		t.Error("SessionStart and Stop put the child in the same state, so comparing " +
			"across the boundary cannot distinguish them, which is the entire point " +
			"of the check")
	}
}

// childrenSnapshot is the map Children builds, keyed by session, without the
// query plumbing: Children goes through the writer loop and this engine has
// none.
func (e *Engine) childrenSnapshot() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, c := range e.children {
		out[c.SessionID] = map[string]any{
			"state":         c.State,
			"since_seconds": time.Since(c.Since).Seconds(),
		}
	}
	return out
}
