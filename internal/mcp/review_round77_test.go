package mcp

import (
	"testing"
)

// A resuming subscriber that states a cursor of zero has a POSITION, at the
// beginning, and a ring floor above it means the beginning is gone. Read
// through the zero-means-everything clamp, that read succeeded, returned what
// the ring still held, and reported no gap: a question whose event had left
// the ring was never resynced and the stream carried on from the present with
// nothing to announce it.
func TestAReconnectFromZeroResyncsWhenTheRingHasPassedIt(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	eng.SetRingCap(4)
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	toolCall(t, srv, "register", map[string]any{"name": "busy", "cwd": t.TempDir()})
	// The question that must not be lost, sent early.
	toolCall(t, srv, "send", map[string]any{
		"token": asker["token"], "to": "busy", "type": "question", "body": "the one that matters",
	})
	// Push the ring floor well past it.
	toolCall(t, srv, "register", map[string]any{"name": "other", "cwd": t.TempDir()})
	for i := 0; i < 10; i++ {
		toolCall(t, srv, "send", map[string]any{
			"token": asker["token"], "to": "other", "type": "notify", "body": "filler",
		})
	}

	_, tooOld := s.missedFor(t.Context(), 0)
	if !tooOld {
		t.Fatal("a reconnect stating a cursor of zero, with the ring floor long past it, " +
			"reported no gap: the resync never runs and a question that arrived before the " +
			"floor is announced by nothing")
	}
}
