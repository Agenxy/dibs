package core

import (
	"testing"
	"time"
)

// R8-2, on the resume path: a same-nonce register that returns to a thread
// bound earlier is a change, and the current thread follows it.
func TestResumingOnAnEarlierThreadMakesItCurrent(t *testing.T) {
	const (
		a = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		b = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	for i, o := range []*Op{
		{Kind: OpRegister, Name: "r", NewToken: "tok", AgentKind: KindPersistent, Nonce: "n", SessionAlias: a, V7Semantics: true},
		{Kind: OpAckBoard, Token: "tok", SessionAlias: b, V7Semantics: true},
	} {
		if _, _, err := s.Apply(o, t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal("setup:", err)
		}
	}
	if s.Agents["r"].CurrentSession != b {
		t.Fatalf("setup: current is %q after moving to b, so the return below proves nothing", s.Agents["r"].CurrentSession)
	}
	res, _, err := s.Apply(&Op{Kind: OpRegister, Name: "r", Nonce: "n", NewToken: "tok2", SessionAlias: a, V7Semantics: true}, t0.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if res["resumed"] != true {
		t.Fatalf("setup: the same-nonce register did not take the resume path (%v)", res)
	}
	if s.Agents["r"].CurrentSession != a {
		t.Errorf("after resuming on thread a the current thread is %q: the resume read a return "+
			"to a known thread as no change and the wake still targets b", s.Agents["r"].CurrentSession)
	}
}
