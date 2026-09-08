package mcp

import (
	"testing"
	"time"
)

// The pre-replay standing check is one instant; the replay is a loop with
// I/O in it. An agent that moves to another session part way through catch-up
// must not be handed the rest of the gap as inbox wakes for the session it
// left. replayGap re-reads the standing before each send.
func TestAReplayStopsWakingASessionTheAgentLeavesMidReplay(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	nonce := "n-busy-0123456789abcdef"
	busy := toolCall(t, srv, "register", map[string]any{
		"name": "busy", "nonce": nonce, "session_id": "host-a", "cwd": t.TempDir(),
	})
	token := busy["token"].(string)
	cursor := busy["serial"]
	// Two questions waiting in the gap, so the replay has more than one event
	// to write and the move can land between them.
	for i := 0; i < 2; i++ {
		toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "busy", "type": "question", "body": "waiting"})
	}
	// The agent moves to session B the instant the replay begins, before it
	// writes the gap.
	moved := make(chan struct{})
	s.duringReplay = func() {
		toolCall(t, srv, "register", map[string]any{
			"name": "busy", "nonce": nonce, "session_id": "host-b", "cwd": t.TempDir(),
		})
		close(moved)
		_ = eng
	}
	// A's stream reconnects from its old cursor, still naming session A.
	inA := openListenFrom(t, srv, token, "host-a", cursor)
	if awaitUpdate(inA, "dibs://inbox") {
		t.Fatal("the stream for session A was handed the gap after the agent moved to B " +
			"mid-replay: the check was made once, before the move")
	}
	// The seam must actually have fired, or the assertion above is vacuous.
	select {
	case <-moved:
	case <-time.After(2 * time.Second):
		t.Fatal("the replay seam never ran, so the mid-replay move was not exercised")
	}
}
