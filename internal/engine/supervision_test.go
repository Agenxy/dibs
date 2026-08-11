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
// person deletes. Most sessions on a machine have no lane.
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
// So the child counts for itself and Lanes uses the counter. Without it, an
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
