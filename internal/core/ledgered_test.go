package core

import "testing"

// Every op that changes replayable state must advance the serial.
//
// This is the rule from AGENTS.md ("an op is ledgered iff it changed replayable
// state") stated as a test, because stating it in prose has now failed three
// times. The engine ledgers exactly when the serial moved, so an op that
// mutates and does not call finish() is applied in memory, acknowledged to the
// caller, never written down, and undone by the next restart.
//
// All three escapes were ops that return early from the switch at the top of
// Apply, before the finishing path every ordinary op goes through:
//
//	prune              closed agents, then returned; the human was told it
//	                   worked and the agents came back holding old tokens
//	claim_coordinator  granted the role in memory; a restart demoted it, past
//	                   five passing unit tests
//	prune_own          the same as prune, added afterwards, with prune's own
//	                   comment about it three functions further down the file
//
// In-process state is exactly what an unledgered mutation gets right, which is
// why every ordinary assertion passes and only the serial catches it.
func TestEveryMutatingOpAdvancesTheSerial(t *testing.T) {
	for _, tc := range []struct {
		name string
		// build returns the op to measure, having set up whatever it needs.
		build func(t *testing.T, s *State) *Op
	}{
		{
			name: "prune_own",
			build: func(t *testing.T, s *State) *Op {
				parent := reg(t, s, "parent", "tok-parent", t0)
				res := spawnChild(t, s, parent.Token, parent.ID, "nonce-child-0123456789abcdef")
				mustApply(t, s, &Op{Kind: OpSignOff, Token: "tok-helper"}, t0)
				return &Op{Kind: OpPruneOwn, Token: parent.Token, To: res["agent_id"].(string)}
			},
		},
		{
			name: "prune",
			build: func(t *testing.T, s *State) *Op {
				a := reg(t, s, "gone", "tok-gone", t0)
				mustApply(t, s, &Op{Kind: OpSignOff, Token: a.Token}, t0)
				return &Op{Kind: OpPrune, To: a.ID}
			},
		},
		{
			name: "claim_coordinator",
			build: func(t *testing.T, s *State) *Op {
				a := regPersistent(t, s, "boss", "tok-boss", "boss-nonce-0123456789abcdef", t0)
				return &Op{Kind: OpClaimCoordinator, Token: a.Token, ClaimVerified: true}
			},
		},
		{
			name: "grant_role",
			build: func(t *testing.T, s *State) *Op {
				a := regPersistent(t, s, "boss", "tok-boss", "boss-nonce-0123456789abcdef", t0)
				return &Op{Kind: OpGrantRole, To: a.ID, Mode: RoleCoordinator}
			},
		},
		{
			name: "declare",
			build: func(t *testing.T, s *State) *Op {
				a := reg(t, s, "worker", "tok-worker", t0)
				// The awareness gate: declaring before reading the board is
				// refused, so this is setup, not ceremony.
				mustApply(t, s, &Op{Kind: OpAckBoard, Token: a.Token}, t0)
				return &Op{Kind: OpSetSlot, Token: a.Token, Text: "some work"}
			},
		},
		{
			name: "sign_off",
			build: func(t *testing.T, s *State) *Op {
				a := reg(t, s, "worker", "tok-worker", t0)
				return &Op{Kind: OpSignOff, Token: a.Token}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewState("n1", DefaultLimits())
			op := tc.build(t, s)
			before := s.Serial

			if _, _, err := s.Apply(op, t0); err != nil {
				t.Fatalf("setup: the op this test measures was refused: %v", err)
			}

			if s.Serial == before {
				t.Errorf("%s changed the board without advancing the serial, so the engine "+
					"will not ledger it: the caller is told it worked and the next restart "+
					"replays a board where it never happened", tc.name)
			}
		})
	}
}
