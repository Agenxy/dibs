package engine

import (
	"testing"
	"time"
)

// A child whose state did not change keeps the age of that state.
//
// noteChild resets Since only when the STATE changes, which is correct: the
// field means "how long has it been in this state". But no caller sets Since on
// an incoming event, and mergeChild did not carry the previous one, so an event
// that left the state alone merged a zero time. The supervision sweep then
// reported since_seconds of 9223372036 (about 292 years) for a child that had
// been running for milliseconds. Two ordinary lifecycle events in a row did it.
//
// A wildly wrong age is worse than a missing one on this surface: the sweep uses
// state age to decide whether a child is stuck, and an operator reads it to
// decide whether to intervene.
func TestAnUnchangedChildStateKeepsItsAge(t *testing.T) {
	started := time.Now().Add(-90 * time.Second)
	prev := Child{SessionID: "s", State: "running", Since: started, Progress: 1}

	// A later event of the same state, carrying no Since: what every caller sends.
	merged := mergeChild(prev, Child{SessionID: "s", State: "running", Progress: 2})

	if merged.Since.IsZero() {
		t.Fatal("Since was dropped, so the sweep would report an age measured from " +
			"the zero time: about 292 years")
	}
	if !merged.Since.Equal(started) {
		t.Errorf("Since = %v, want the original %v: the state did not change, so its "+
			"age must not restart", merged.Since, started)
	}
	if merged.Progress != 2 {
		t.Errorf("progress = %d, want 2", merged.Progress)
	}

	// An event that DOES carry a Since wins: a genuine state change is timed by
	// the caller, and preserving the old one would freeze the age.
	fresh := time.Now()
	if got := mergeChild(prev, Child{SessionID: "s", State: "blocked", Since: fresh}); !got.Since.Equal(fresh) {
		t.Errorf("Since = %v, want the incoming %v", got.Since, fresh)
	}
}
