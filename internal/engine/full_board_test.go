// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/humanask"
)

type fullBoardProber map[int]bool

func (p fullBoardProber) Alive(pid int) bool { return p[pid] }

// Use the public writer ingress, not a victim setter or Apply alone: the
// production decision needs a process observation before admission.
func TestFullBoardRegisterDisplacesOldestIdleAgent(t *testing.T) {
	limits := core.DefaultLimits()
	limits.MaxAgents, limits.MaxPersistentAgents = 2, 2
	st := core.NewState("full-board-test", limits)
	now := time.Now()
	for i, id := range []string{"oldest", "newer"} {
		_, _, err := st.Apply(&core.Op{
			Kind: core.OpRegister, Name: id, NewToken: "token-" + id,
			AgentKind: core.KindPersistent, Nonce: "nonce-" + id, PID: 100 + i,
		}, now.Add(time.Duration(i-4)*time.Hour))
		if err != nil {
			t.Fatal("setup registration:", err)
		}
		st.Agents[id].Status = core.StatusDormant
	}
	if len(st.Agents) != 2 || !st.Agents["oldest"].LastCoordination.Before(st.Agents["newer"].LastCoordination) {
		t.Fatal("setup: full board does not contain ordered dormant agents")
	}
	e := New(st, &memLedger{}, fullBoardProber{})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	t.Cleanup(func() { cancel(); <-e.done })
	result, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: "newcomer", AgentKind: core.KindPersistent,
		Nonce: "nonce-newcomer",
	})
	if err != nil {
		t.Fatalf("a full board with a verified-dead dormant seat refused registration: %v", err)
	}
	if result["agent_id"] != "newcomer" {
		t.Fatalf("newcomer was not admitted: %v", result)
	}
	got, err := e.query(ctx, func() core.Result {
		return core.Result{"oldest": st.Agents["oldest"].Status, "newer": st.Agents["newer"].Status}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["oldest"] != core.StatusClosed || got["newer"] != core.StatusDormant {
		t.Fatalf("registration did not close exactly the oldest dormant seat: %v", got)
	}
}

type capacityProbeFunc func(int) bool

func (f capacityProbeFunc) Alive(pid int) bool { return f(pid) }

func capacityState(t *testing.T, ids ...string) *core.State {
	t.Helper()
	limits := core.DefaultLimits()
	limits.MaxAgents, limits.MaxPersistentAgents = len(ids), len(ids)
	st := core.NewState("capacity-test", limits)
	for i, id := range ids {
		_, _, err := st.Apply(&core.Op{Kind: core.OpRegister, Name: id, NewToken: "token-" + id,
			AgentKind: core.KindPersistent, Nonce: "nonce-" + id, PID: 100 + i},
			time.Now().Add(time.Duration(i-100)*time.Hour))
		if err != nil {
			t.Fatal("seed:", err)
		}
		st.Agents[id].Status = core.StatusDormant
	}
	return st
}

func runCapacityEngine(t *testing.T, e *Engine) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	go e.Run(ctx)
	t.Cleanup(func() { cancel(); <-e.done })
	return ctx
}

func capacityRegister() *core.Op {
	return &core.Op{Kind: core.OpRegister, Name: "newcomer", Nonce: "nonce-newcomer"}
}

func TestFullBoardDisplacementRequiresReaderBeforeRegisterAppend(t *testing.T) {
	st := capacityState(t, "old")
	led := &memLedger{}
	e := New(st, led, nil)
	ctx := runCapacityEngine(t, e)
	if _, err := e.Do(ctx, capacityRegister()); err != nil {
		t.Fatal(err)
	}
	onLoop(t, ctx, e, func(*core.State) {
		fenced, recorded := false, false
		var kinds []string
		for _, op := range led.ops {
			kinds = append(kinds, op.Kind)
			if op.Kind == core.OpRequireV17Features {
				for _, feature := range op.RequiredFeatures {
					if feature == core.FeatureFullBoardPolicy {
						fenced = true
					}
				}
			}
			if op.Kind == core.OpRegister && op.Displaced != nil {
				recorded = true
				if !fenced {
					t.Error("displacement not fenced before its append")
				}
			}
		}
		if !recorded || !fenced {
			t.Errorf("displacement not fenced before its append: %v", kinds)
		}
	})
}

func TestFullBoardProtectsRolesAliveAndStaleRows(t *testing.T) {
	st := capacityState(t, "human", "coordinator", "admin", "live", "stale", "unknown", "dead")
	st.Agents["coordinator"].Role, st.Agents["admin"].Role = "coordinator", "admin"
	st.Agents["stale"].Status = core.StatusStale
	st.Agents["unknown"].PID = 0
	// The private nonce, not an admin role, identifies the protected human.
	st.Nonces[humanNonce()] = "human"
	e := New(st, &memLedger{}, fullBoardProber{103: true})
	ctx := runCapacityEngine(t, e)
	op := capacityRegister()
	if _, err := e.Do(ctx, op); err != nil {
		t.Fatal(err)
	}
	if op.Displaced == nil || op.Displaced.ID != "dead" || op.Displaced.Process != core.ProcessDead {
		t.Fatalf("protected row displaced or unknown ranked before dead: %+v", op.Displaced)
	}
	onLoop(t, ctx, e, func(s *core.State) {
		for _, id := range []string{"human", "coordinator", "admin", "live", "stale", "unknown"} {
			if s.Agents[id].Status == core.StatusClosed {
				t.Errorf("protected/nonselected %s closed", id)
			}
		}
	})
}

func TestFullBoardUnknownHostIsNotProbedAndRanksByRealActivity(t *testing.T) {
	st := capacityState(t, "older", "recent")
	for _, row := range st.Agents {
		row.Agent = &core.AgentInfo{HostID: "foreign-host"}
	}
	var probes atomic.Int32
	e := New(st, &memLedger{}, capacityProbeFunc(func(int) bool { probes.Add(1); return true }))
	ctx := runCapacityEngine(t, e)
	// Boot grace is equal across rows and must not replace real activity.
	onLoop(t, ctx, e, func(s *core.State) {
		at := time.Now()
		e.bootGrace = map[string]bootEvidence{}
		for id, row := range s.Agents {
			e.seen[id], e.bootGrace[id] = at, bootEvidence{at: at, created: row.CreatedSerial}
		}
	})
	op := capacityRegister()
	if _, err := e.Do(ctx, op); err != nil {
		t.Fatal(err)
	}
	if probes.Load() != 0 || op.Displaced == nil || op.Displaced.ID != "older" || op.Displaced.Process != core.ProcessUnknown {
		t.Fatalf("foreign PID or boot-grace ranking: probes=%d decision=%+v", probes.Load(), op.Displaced)
	}
}

func TestFullBoardPersistentCeilingNeedsPersistentVictim(t *testing.T) {
	st := capacityState(t, "ephemeral", "persistent")
	st.Limits.MaxAgents, st.Limits.MaxPersistentAgents = 3, 1
	st.Agents["ephemeral"].Kind = core.KindEphemeral
	e := New(st, &memLedger{}, fullBoardProber{})
	ctx := runCapacityEngine(t, e)
	op := capacityRegister()
	if _, err := e.Do(ctx, op); err != nil {
		t.Fatal(err)
	}
	if op.Displaced == nil || op.Displaced.ID != "persistent" {
		t.Fatalf("wrong ceiling victim: %+v", op.Displaced)
	}
}

func TestFullBoardRefuseAndProtectedOnlyRemainHonest(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "protected", true: "refuse"}[refuse], func(t *testing.T) {
			st := capacityState(t, "old")
			if !refuse {
				st.Agents["old"].Role = "coordinator"
			}
			e := New(st, &memLedger{}, fullBoardProber{})
			if refuse {
				if err := e.SetFullBoardPolicyDefault(core.FullBoardRefuse); err != nil {
					t.Fatal(err)
				}
			}
			ctx := runCapacityEngine(t, e)
			_, err := e.Do(ctx, capacityRegister())
			var failure *core.Error
			if !errors.As(err, &failure) || failure.Code != "E_AGENT_LIMIT" || failure.Hint == "" {
				t.Fatalf("refusal: %v", err)
			}
			onLoop(t, ctx, e, func(s *core.State) {
				if s.Agents["old"].Status != core.StatusDormant {
					t.Fatal("refusal closed old row")
				}
			})
		})
	}
}

func TestFullBoardRecoveryAndCallerFieldsNeverDisplace(t *testing.T) {
	st := capacityState(t, "old")
	e := New(st, &memLedger{}, fullBoardProber{})
	ctx := runCapacityEngine(t, e)
	op := &core.Op{Kind: core.OpRegister, Name: "old", Nonce: "nonce-old", FullBoardPolicy: core.FullBoardDisplaceIdle,
		Displaced: &core.AgentDisplacement{ID: "old"}}
	res, err := e.Do(ctx, op)
	if err != nil || res["agent_id"] != "old" || op.Displaced != nil {
		t.Fatalf("recovery displaced: %v %v %+v", res, err, op.Displaced)
	}
}

func TestFullBoardRecoveryKeepsHistoricalCapacityException(t *testing.T) {
	for _, policy := range []string{core.FullBoardRefuse, core.FullBoardDisplaceIdle} {
		t.Run(policy, func(t *testing.T) {
			st := capacityState(t, "old", "other")
			st.Agents["old"].Status = core.StatusArchived
			st.Agents["old"].Nonce = ""
			st.Limits.MaxAgents, st.Limits.MaxPersistentAgents = 1, 1
			e := New(st, &memLedger{}, nil)
			if err := e.SetFullBoardPolicyDefault(policy); err != nil {
				t.Fatal(err)
			}
			ctx := runCapacityEngine(t, e)
			op := &core.Op{Kind: core.OpRegister, Name: "old", Nonce: "nonce-old"}
			res, err := e.Do(ctx, op)
			if err != nil || res["agent_id"] != "old" || op.Displaced != nil {
				t.Fatalf("existing recovery changed: %v %v", res, err)
			}
			onLoop(t, ctx, e, func(s *core.State) {
				if s.Agents["other"].Status != core.StatusDormant {
					t.Fatal("recovery displaced another seat")
				}
			})
		})
	}
}

func TestFullBoardEqualActivityBreaksTiesByID(t *testing.T) {
	st := capacityState(t, "zeta", "alpha")
	st.Agents["alpha"].LastCoordination = st.Agents["zeta"].LastCoordination
	e := New(st, &memLedger{}, nil)
	ctx := runCapacityEngine(t, e)
	op := capacityRegister()
	if _, err := e.Do(ctx, op); err != nil {
		t.Fatal(err)
	}
	if op.Displaced == nil || op.Displaced.ID != "alpha" {
		t.Fatalf("activity tie: %+v", op.Displaced)
	}
}

func TestFullBoardProbeLeavesWriterFreeAndRepicksOnlyOnce(t *testing.T) {
	for _, races := range []int{1, 2} {
		t.Run(map[int]string{1: "repick", 2: "second-race-refuses"}[races], func(t *testing.T) {
			st := capacityState(t, "oldest", "second")
			var e *Engine
			var ctx context.Context
			var changed int
			var secondProbes int
			probe := capacityProbeFunc(func(pid int) bool {
				if pid == 101 {
					secondProbes++
				}
				// Re-entering the writer here would deadlock if probing held it.
				if changed == 0 || races == 2 && changed == 1 && pid == 101 && secondProbes == 2 {
					id := "oldest"
					if changed == 1 {
						id = "second"
					}
					_, err := e.query(ctx, func() core.Result { st.Agents[id].Status = core.StatusActive; return core.Result{} })
					if err != nil {
						t.Error(err)
					}
					changed++
				}
				return false
			})
			e = New(st, &memLedger{}, probe)
			ctx = runCapacityEngine(t, e)
			op := capacityRegister()
			_, err := e.Do(ctx, op)
			if races == 1 {
				if err != nil || op.Displaced == nil || op.Displaced.ID != "second" {
					t.Fatalf("repick: %v %+v", err, op.Displaced)
				}
			} else if err == nil || !strings.Contains(err.Error(), "E_AGENT_LIMIT") {
				t.Fatalf("second race: %v", err)
			}
		})
	}
}

type capacityNotifier struct{ notices chan humanask.Message }

func (n capacityNotifier) Available() bool { return true }
func (n capacityNotifier) Ask(m humanask.Message) (humanask.Answer, error) {
	n.notices <- m
	return humanask.Answer{}, nil
}

func TestFullBoardAffectedMailNotifiesBeforeHumanRowExists(t *testing.T) {
	st := capacityState(t, "old")
	st.Messages[99] = &core.Message{Serial: 99, To: "old", From: "gone-sender", Type: core.MsgNotify, State: core.MsgStatePending, Body: "PRIVATE BODY"}
	n := capacityNotifier{make(chan humanask.Message, 1)}
	e := New(st, &memLedger{}, nil)
	e.SetHumanNotifier(n)
	ctx := runCapacityEngine(t, e)
	if _, err := e.Do(ctx, capacityRegister()); err != nil {
		t.Fatal(err)
	}
	select {
	case notice := <-n.notices:
		if !strings.Contains(notice.Body, "1 undelivered") || strings.Contains(notice.Body, "PRIVATE BODY") {
			t.Fatalf("metadata notice: %+v", notice)
		}
	case <-ctx.Done():
		t.Fatal("missing native notice when the human has no mailbox yet")
	}
}

func TestFullBoardNoticeOnlyForAffectedMailAndOldNonceRefused(t *testing.T) {
	for _, mail := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "affected"}[mail], func(t *testing.T) {
			st := capacityState(t, "old", "human")
			st.Nonces[humanNonce()] = "human"
			if mail {
				st.Messages[99] = &core.Message{Serial: 99, To: "old", From: "human", Type: core.MsgQuestion, State: core.MsgStatePending, Body: "PRIVATE BODY"}
			}
			n := capacityNotifier{make(chan humanask.Message, 2)}
			e := New(st, &memLedger{}, fullBoardProber{})
			e.SetHumanNotifier(n)
			ctx := runCapacityEngine(t, e)
			var relay <-chan HumanNotice
			if !mail {
				var detach func()
				relay, detach = e.AttachHumanRelay()
				defer detach()
			}
			events, detachEvents := e.Subscribe(0)
			defer detachEvents()
			op := capacityRegister()
			if _, err := e.Do(ctx, op); err != nil {
				t.Fatal(err)
			}
			if mail {
				select {
				case notice := <-n.notices:
					if strings.Contains(notice.Body, "PRIVATE") || !strings.Contains(notice.Body, "1 unanswered") || notice.Type != core.MsgNotify {
						t.Fatalf("notice: %+v", notice)
					}
				case <-ctx.Done():
					t.Fatal("no metadata notice")
				}
			}
			found := false
			for !found {
				select {
				case event := <-events:
					if event.Type == "agent.displaced" {
						found = true
						if event.Agent != "old" || event.To != "human" || event.Data["newcomer"] != "newcomer" {
							t.Fatalf("audit: %+v", event)
						}
					}
				case <-ctx.Done():
					t.Fatal("no displacement audit")
				}
			}
			select {
			case notice := <-relay:
				t.Fatalf("empty recycling raised notice: %+v", notice)
			default:
			}
			_, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "old", Nonce: "nonce-old"})
			var failure *core.Error
			if !errors.As(err, &failure) || failure.Code != "E_AGENT_DISPLACED" || !strings.Contains(failure.Hint, "nonce") {
				t.Fatalf("old nonce: %v", err)
			}
			onLoop(t, ctx, e, func(s *core.State) {
				if mail && s.Messages[99].State != core.MsgStateExpiredDead {
					t.Fatal("ask was not pruned")
				}
				if len(s.Messages) != map[bool]int{false: 0, true: 1}[mail] {
					t.Fatal("displacement created inbox mail")
				}
			})
			select {
			case notice := <-n.notices:
				t.Fatalf("unexpected/duplicate notice %+v", notice)
			default:
			}
		})
	}
}
