package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

// human_unlock must not raise a sheet for a caller it cannot name.
//
// The sentence on that sheet is the whole control: it is written by the daemon
// precisely so a caller cannot write its own, and it names the requesting
// agent. CallerName ANSWERS for an unknown token, with "an unidentified
// caller", so this raised a system prompt on the operator's screen attributed
// to that phrase. Anything holding the coordination secret could make the
// machine ask its human to approve something, and the field that says who is
// asking said nobody.
//
// Physical approval was still required, so this was never a biometric bypass.
// The attribution was false, and the attribution is what the human decides on.
// SECURITY.md claims the requester is resolved "from the authenticated token";
// nothing authenticated it. Found by the pre-release review.
func TestHumanUnlockRefusesACallerItCannotName(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "test", box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = led.Close() })
	st := core.NewState("test", core.DefaultLimits())
	e := engine.New(st, led, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// Setup must hold: the token really must not resolve, or this passes for
	// the wrong reason.
	if e.CallerIsKnown(ctx, "not-a-real-token") {
		t.Fatal("setup: the bogus token resolves to an agent")
	}

	s := &Server{eng: e}
	got, err := s.humanUnlock(ctx, &toolArgs{Token: "not-a-real-token"})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if ok, _ := got["ok"].(bool); ok {
		t.Fatal("an unauthenticated caller was allowed to raise a presence prompt " +
			"on the operator's screen, attributed to nobody")
	}
	if why, _ := got["why"].(string); !strings.Contains(why, "token") {
		t.Errorf("the refusal does not say what is missing: %v", got)
	}
}
