package engine

import (
	"context"
	"slices"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// R15-2: correcting an agent's cwd starts discovery of the corrected
// repository, as registration does.
func TestACorrectedLocationIsDiscovered(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	var seen []string
	e.OnRepoSeen(func(repo string) { seen = append(seen, repo) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	res, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "w", AgentKind: core.KindPersistent, Nonce: "n-w-0123456789abcdef", Agent: &core.AgentInfo{CWD: "/wrong/place"}})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if !slices.Contains(seen, "/wrong/place") {
		t.Fatal("setup: registration did not report its location, so the correction below proves nothing")
	}
	tok, _ := res["token"].(string)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpUpdate, Token: tok, Description: "d", Agent: &core.AgentInfo{CWD: "/right/place"}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(seen, "/right/place") {
		t.Errorf("update(cwd) accepted the correction and started no discovery of it (seen %v): "+
			"matching stays unavailable for the repository the agent actually works in", seen)
	}
}
