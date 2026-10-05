package engine

import (
	"fmt"
	"github.com/agenxy/dibs/internal/core"
	"time"
)

// applyAndLedger applies an op and ledgers it iff the serial advanced.
// Persistence failure is fail-stop (SPEC §4).
func (e *Engine) applyAndLedger(op *core.Op, now time.Time) (core.Result, error) {
	e.stampReviewRetention(op, now)
	before := e.state.Serial
	res, evs, err := e.state.Apply(op, now)
	if err != nil {
		// A handler that advanced the serial and THEN failed has committed a
		// transition nobody will ever record: the op is not appended, so the
		// ledger skips that serial forever and every later one is off by one.
		// A real board did exactly this: serial 447 allocated, never written,
		// and the daemon then refused to replay its own ledger on restart.
		//
		// Fail-stop for the same reason a persistence failure does (SPEC §4).
		// The in-memory state is already wrong; continuing would write more ops
		// on top of a divergence, and restarting replays cleanly from the last
		// good line. Loud and immediate beats silent and permanent.
		if e.state.Serial != before {
			panic(fmt.Sprintf(
				"dibs: %s advanced the serial %d→%d then failed (%v). "+
					"a transition was committed but cannot be ledgered (fail-stop, SPEC §4)",
				op.Kind, before, e.state.Serial, err,
			))
		}
		return nil, err
	}
	// ONE op, ONE serial. A handler that allocates two writes only its last,
	// leaving a hole, and the record at the hole is a state transition that
	// happened and was never recorded, so every later replay reconstructs a
	// different board than the one that ran.
	//
	// That is not theoretical. This board reached a state where `sign_off`
	// appeared twice for one agent with no re-registration between them: live it
	// resolved a token that replay cannot see (Op.Token is `json:"-"`, so replay
	// resolves by agent id instead), and the op that must have re-created that
	// agent is sitting in one of the holes. The daemon then refused to start,
	// correctly, and with no way back until `dibs admin repair-ledger` existed.
	//
	// Fail-stop for the same reason the advance-then-fail case above does, and
	// louder than the gap WARNING at replay: by the time replay warns, the
	// transition is already lost. Here it is still on the stack, and the op kind
	// that did it is in the message.
	if e.state.Serial > before+1 {
		panic(fmt.Sprintf(
			"dibs: %s advanced the serial %d→%d: one op must allocate exactly one "+
				"serial, or the ledger gets a hole where a real transition happened "+
				"(fail-stop, SPEC §4)",
			op.Kind, before, e.state.Serial,
		))
	}
	if e.state.Serial != before {
		if lerr := e.led.Append(e.state.Serial, now, op); lerr != nil {
			panic(fmt.Sprintf("dibs: ledger persistence failure (fail-stop, SPEC §4): %v", lerr))
		}
		e.observeNameAliases(op, evs)
		e.publish(evs)
	}
	if op.Kind == core.OpPutBlob {
		e.protectBlobRegistration(op.Blob)
	}
	return res, nil
}
