package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// `dibs` is the identity the daemon reports its own faults under, on a board
// where an agent reading "Dibs found a fault" has no way to check who wrote
// it. The nonce guard reserves it against a caller PRESENTING that nonce; a
// v0.0.6 archive-and-recovery blanks the nonce field while keeping the index,
// and the row was then recoverable by a name and a session id that are both
// constants in this repository.
func TestTheDaemonsOwnReportingRowCannotBeRecovered(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// The daemon's row, as selfreport registers it.
	if _, err := e.Do(ctx, &core.Op{
		// HumanMint is the one registration allowed to BE this identity, which
		// is how dibsAgent creates it.
		HumanMint: true,
		Kind:      core.OpRegister, Name: dibsName, NewToken: "sys-1",
		AgentKind: core.KindPersistent,
		Nonce:     dibsNonce(), SessionID: dibsNonce(), NoProcess: true,
	}); err != nil {
		t.Fatal("setup:", err)
	}
	id := st.Nonces[dibsNonce()]
	if id == "" {
		t.Fatal("setup: the daemon row is not in the nonce index")
	}
	// The state a v0.0.6 archive-and-recovery leaves behind.
	st.Agents[id].Nonce = ""

	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "dibs", NewToken: "stolen", SessionID: dibsNonce(),
	})
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) || ce.Code != "E_NEEDS_NONCE" {
		t.Fatalf("a name-and-session register landed on the daemon's reporting row: %v %v: the "+
			"caller now speaks as the thing that reports faults about the machine", res, err)
	}
}
