package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A board-only subscriber whose position fell outside the ring was handed
// nothing on a refill, with its loss mark already cleared, and stayed on a
// stale board until the next change happened to reach it. One board notice
// says look again; the board resource is a snapshot, so that is all a board
// subscriber needs.
func TestABoardOnlySubscriberPastTheRingIsToldToLookAgain(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	eng.SetRingCap(4)
	ctx := context.Background()
	asker := toolCall(t, srv, "register", map[string]any{"name": "asker", "cwd": t.TempDir()})
	third := toolCall(t, srv, "register", map[string]any{"name": "third", "cwd": t.TempDir()})
	// A position past the first serial: a position of one asks the ring
	// from zero, which the engine reads as "from wherever the ring starts"
	// rather than as a gap.
	cursor, _ := third["serial"].(float64)
	for i := 0; i < 8; i++ {
		toolCall(t, srv, "send", map[string]any{"token": asker["token"], "to": "third", "type": "notify", "body": "filler"})
	}
	sub, cancel := eng.SubscribeTracked(uint64(cursor))
	defer cancel()
	rec := httptest.NewRecorder()
	p := &pumpState{
		s: s, ctx: ctx, stream: sseStream{w: rec, fl: rec}, sub: sub,
		last: uint64(cursor), lastSub: 0, subID: json.RawMessage(`"listen"`),
		wants: func() (string, bool, bool) { return "", false, true },
	}
	sub.MarkLost()
	if !p.refill() {
		t.Fatal("the refill reported the stream closed")
	}
	if got := rec.Body.String(); !strings.Contains(got, `"uri":"dibs://board"`) {
		t.Fatalf("a board-only subscriber past the ring was handed nothing on its refill:\n%s", got)
	}
	_ = core.Event{}
}
