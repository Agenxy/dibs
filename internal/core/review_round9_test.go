package core

import (
	"strings"
	"testing"
	"time"
)

// R9-2: releasing the session releases the current one too, or both wake
// routes keep targeting the session the caller relinquished.
func TestReleasingTheSessionReleasesTheCurrentOne(t *testing.T) {
	const a = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "r", NewToken: "tok", AgentKind: KindPersistent, Nonce: "n", SessionAlias: a, V7Semantics: true}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	if s.Agents["r"].CurrentSession != a {
		t.Fatal("setup: the alias did not become current, so the release below proves nothing")
	}
	res, _, err := s.Apply(&Op{Kind: OpUpdate, Token: "tok", Description: "d", ReleaseSession: true, V7Semantics: true}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if res["session_released"] != true {
		t.Fatalf("setup: nothing was released (%v)", res)
	}
	if cur := s.Agents["r"].CurrentSession; cur != "" {
		t.Errorf("after release_session the current session is still %q: the call reported the "+
			"release and the next wake resumes the session the caller gave up", cur)
	}
}

// R9-3: an explicit bind_session is the session to wake.
func TestAnExplicitBindBecomesTheCurrentSession(t *testing.T) {
	const (
		a = "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
		b = "01a00042-2222-7f60-81cc-6ab1298d76ec"
	)
	s := NewState("test", DefaultLimits())
	t0 := time.Now()
	if _, _, err := s.Apply(&Op{Kind: OpRegister, Name: "r", NewToken: "tok", AgentKind: KindPersistent, Nonce: "n", SessionAlias: a, V7Semantics: true}, t0); err != nil {
		t.Fatal("setup:", err)
	}
	res, _, err := s.Apply(&Op{Kind: OpBindSession, Token: "tok", SessionID: b, V7Semantics: true}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if res["session_id"] != b {
		t.Fatalf("setup: the bind did not report %s (%v)", b, res)
	}
	if cur := s.Agents["r"].CurrentSession; cur != b {
		t.Errorf("bind_session reported %s and the current session is %q: the wake resumes "+
			"the previous binding while the caller was told the new one took", b, cur)
	}
}

// R9-4: a registration given a minted nonce is not told it has no way back.
func TestADefaultRegistrationIsNotToldItCannotBeRecovered(t *testing.T) {
	s := NewState("test", DefaultLimits())
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "w", NewToken: "tok", AgentKind: KindPersistent,
		MintedNonce: "minted-0123456789abcdef0123456789abcdef", V7Semantics: true,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res["nonce"] == nil {
		t.Fatal("setup: no nonce was returned, so there is no contradiction to test")
	}
	advice, _ := res["recovery"].(string)
	if strings.Contains(advice, "cannot be reclaimed") || strings.Contains(advice, "Re-register with a nonce") {
		t.Errorf("the reply returns a working nonce and says: %q\n  following that advice registers "+
			"a fresh nonce under the same name, which is the sibling mailbox this release exists to prevent", advice)
	}
}
