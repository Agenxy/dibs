package core

import (
	"testing"
	"time"
)

// R11-1: recovering by a session id makes that id the current session, or
// the wake keeps resuming the thread the agent left.
func TestRecoveringByASessionIDMakesItCurrent(t *testing.T) {
	const (
		b = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		c = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	for i, o := range []*Op{
		// a persistent default: the nonce is minted, so session-based recovery applies
		{Kind: OpRegister, Name: "r", NewToken: "tok", AgentKind: KindPersistent, MintedNonce: "m-0123456789abcdef", SessionAlias: b, V7Semantics: true},
		{Kind: OpAckBoard, Token: "tok", SessionAlias: c, V7Semantics: true},
	} {
		if _, _, err := s.Apply(o, t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal("setup:", err)
		}
	}
	if s.Agents["r"].CurrentSession != c {
		t.Fatal("setup: c did not become current, so the recovery below proves nothing")
	}
	s.Agents["r"].Status = StatusDormant
	// As the ingress shapes it: a persistent register without a nonce is given one.
	res, _, err := s.Apply(&Op{Kind: OpRegister, Name: "r", NewToken: "tok2", AgentKind: KindPersistent, MintedNonce: "m2-0123456789abcdef", SessionID: b, V7Semantics: true}, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res["reattached"] != true || res["agent_id"] != "r" {
		t.Fatalf("setup: the register did not reattach by session id (%v)", res)
	}
	if cur := s.Agents["r"].CurrentSession; cur != b {
		t.Errorf("recovered by session id %s and the current session is %q: the wake resumes "+
			"the thread the agent left", b, cur)
	}
}
