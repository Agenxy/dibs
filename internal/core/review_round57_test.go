package core

import (
	"errors"
	"testing"
	"time"
)

// A same-nonce register inside the TTL that moved the row to a new session
// kept the previous activation's acknowledgement, so the new one could
// claim without a check_in. Awareness belongs to the current activation.
func TestMovingALiveIdentityToANewSessionReArmsTheAwarenessGate(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-1", Nonce: "n-r-0123456789abcdef", AgentKind: KindPersistent, SessionID: "host-111", V7Semantics: true}, now)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tok-1", V7Semantics: true}, now)
	res := mustApply(t, s, &Op{Kind: OpRegister, Name: "r", NewToken: "tok-2", Nonce: "n-r-0123456789abcdef", AgentKind: KindPersistent, SessionID: "host-222", V7Semantics: true}, now.Add(time.Minute))
	if res["resumed"] != true {
		t.Fatalf("setup: the same-nonce register did not resume: %v", res)
	}
	// A live resume keeps the credential it had; the result says which.
	tok, _ := res["token"].(string)
	_, _, err := s.Apply(&Op{Kind: OpClaim, Token: tok, Path: "/work/x", Mode: ClaimExclusive, V7Semantics: true}, now.Add(time.Minute))
	var ce *Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_MUST_ACK_BOARD" {
		t.Fatalf("a new activation claimed without checking in: the previous activation's "+
			"acknowledgement stood for it: %v", err)
	}
}

// An identical retry that states a synthetic primary beside a thread alias
// read as a change on every call, because the primary is never the current
// session while the thread is, and was ledgered every time.
func TestAnIdenticalRetryWithPrimaryAndAliasIsANoOp(t *testing.T) {
	const thread = "01a0696b-8446-7821-a992-9dc7f6a43a25"
	for _, shape := range []struct{ name, session, alias string }{
		{"synthetic primary, thread alias", "host-4242", thread},
		{"thread primary, synthetic alias", thread, "host-4242"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			s := NewState("test", DefaultLimits())
			t0 := time.Unix(1700000000, 0)
			op := func(tok string) *Op {
				return &Op{
					Kind: OpRegister, Name: "worker", AgentKind: KindPersistent, Nonce: "n1",
					NewToken: tok, SessionID: shape.session, SessionAlias: shape.alias, V7Semantics: true,
				}
			}
			mustApply(t, s, op("tok-1"), t0)
			if s.Agents["worker"].CurrentSession != thread {
				t.Fatalf("setup: the current session is %q, want the thread", s.Agents["worker"].CurrentSession)
			}
			before := s.Serial
			_, evs, err := s.Apply(op("tok-2"), t0.Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if len(evs) != 0 || s.Serial != before {
				t.Fatalf("an identical retry emitted %d event(s) and moved the serial %d -> %d: a retry is "+
					"not a change, and this shape is what the bridge sends", len(evs), before, s.Serial)
			}
		})
	}
}
