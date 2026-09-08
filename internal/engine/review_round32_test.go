package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/peerwake"
)

// The socket route took the first address it found among every session the
// agent had ever answered to, so an agent that moved from A to B, with B
// publishing no socket and A's still open, was woken at A; a delivery ends
// the attempt and B stayed asleep. When the current activation is known it
// is the only address tried, as the exec route already does.
func TestTheSocketRouteDoesNotFallBackToAnEarlierActivation(t *testing.T) {
	const (
		a = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		b = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	l := &core.Agent{ID: "mover", SessionID: "host-111", SessionAliases: []string{a}, CurrentSession: b}
	live := map[string]peerwake.Session{a: {SessionID: a}}
	if s, ok := peerSessionIn(live, sessionsOf(l)); ok {
		t.Fatalf("moved to %s, the socket route reaches %s: the activation the agent left is "+
			"woken and the one waiting stays asleep", b, s.SessionID)
	}
	live[b] = peerwake.Session{SessionID: b}
	if s, ok := peerSessionIn(live, sessionsOf(l)); !ok || s.SessionID != b {
		t.Fatalf("with %s listening the socket route reaches %v, %v", b, s.SessionID, ok)
	}
	// A row bound before the current session was recorded keeps the scan.
	old := &core.Agent{ID: "elder", SessionID: "host-222", SessionAliases: []string{a}}
	if s, ok := peerSessionIn(live, sessionsOf(old)); !ok || s.SessionID != a {
		t.Fatalf("a row with no current session recorded is not reached through its alias: %v %v", s.SessionID, ok)
	}
}
