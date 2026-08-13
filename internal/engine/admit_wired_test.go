package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A validation gate that is never called is not a validation gate.
//
// core.Admit was written, unit-tested and documented, and the line that calls
// it never landed, because the script that made the change died between adding
// the function and wiring it up. Every test still passed: the unit tests call
// Admit directly, and the fold tests assert Apply keeps accepting what Admit
// rejects, which is exactly what an unwired gate does too. From the outside the
// suite was green and the rule did nothing.
//
// So this test goes through the ENGINE, the way a caller does. It is the only
// kind of test that can tell "correct and connected" from "correct and dead".
func TestAdmitIsActuallyOnTheIngressPath(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "alpha"})
	if err != nil {
		t.Fatalf("setup: register: %v", err)
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatalf("setup: check_in: %v", err)
	}

	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSpaceOpen, Token: tok, Space: "w", Text: "work",
	}); err != nil {
		t.Fatalf("setup: opening an agent: %v", err)
	}

	_, err = e.Do(ctx, &core.Op{
		Kind: core.OpSpaceAnnounce, Token: tok, Space: "w", Body: "   ",
	})
	if err == nil {
		t.Fatal("an empty announcement reached the ledger: core.Admit is not wired " +
			"into engine.exec, and every unit test of it passes anyway")
	}
	if !strings.Contains(err.Error(), "E_EMPTY_BODY") {
		t.Errorf("rejected for the wrong reason, so the gate may still be absent: %v", err)
	}

	// And a real one still gets through, or the gate is worse than none.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpSpaceAnnounce, Token: tok, Space: "w", Body: "freezing auth/retry.go",
	}); err != nil {
		t.Errorf("a legitimate announcement was refused: %v", err)
	}
}
