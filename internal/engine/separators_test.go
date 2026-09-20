package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// On Windows the guard's question is spelled the way the fold spelled the
// claims. The fold turns `\` into `/` for every path an op carries, on
// Windows only, and the guard query went straight to the engine with its
// separators intact: a stored exclusive claim on `C:/repo` matched nothing
// when asked about `C:\repo\file.go`, and the guard answered allow. Round
// eleven of the pre-release review. Exercised on any machine by turning the
// fold on for the test.
func TestAWindowsGuardQueryIsSpelledLikeTheClaimsItIsComparedWith(t *testing.T) {
	prev := foldsSeparators
	foldsSeparators = true
	t.Cleanup(func() { foldsSeparators = prev })

	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	reg := func(name, session string) string {
		res, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, SessionID: session, Agent: &core.AgentInfo{CWD: `C:\repo`},
		})
		if err != nil {
			t.Fatal(err)
		}
		tok, _ := res["token"].(string)
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
			t.Fatal(err)
		}
		return tok
	}
	holder := reg("holder", "s-holder")
	reg("other", "s-other")
	if res, err := e.Do(ctx, &core.Op{Kind: core.OpClaim, Token: holder, Path: `C:\repo`, Mode: core.ClaimExclusive}); err != nil || res["granted"] != true {
		t.Fatalf("setup: claim: %v %v", res, err)
	}

	res, err := e.GuardPath(ctx, "s-other", `C:\repo\file.go`, `C:\repo`)
	if err != nil {
		t.Fatal(err)
	}
	if res["decision"] != core.GuardDeny {
		t.Errorf("guard(C:\\repo\\file.go) = %v, want deny: the claim was stored as C:/repo and the "+
			"query kept its backslashes", res)
	}
}
