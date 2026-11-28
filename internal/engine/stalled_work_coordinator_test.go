// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestCheckInSurfacesAStalledCodexDeclarationToTheCoordinator(t *testing.T) {
	f := newStalledWorkFixture(t, "idle", "")
	coordinator := f.coordinator(t)
	advanceStalledWorkFixture(t, f, true)
	e, ctx := f.e, f.ctx
	board, err := e.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var state any
	for _, agent := range board["agents"].([]map[string]any) {
		if agent["id"] == "worker" {
			state = agent["work"]
		}
	}
	if state != "stalled" {
		t.Fatalf("setup: board work=%v, want stalled", state)
	}
	r, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: coordinator, MailboxPage: true})
	if err != nil || r["error"] != nil {
		t.Fatalf("coordinator check_in: %v / %v", r, err)
	}
	if !hasStalledWorkMail(r) {
		t.Fatalf("coordinator did not receive the stalled declaration from check_in: %v", r)
	}
	r = f.checkIn(t, coordinator)
	if hasStalledWorkMail(r) {
		t.Fatalf("the same stalled declaration was announced twice: %v", r)
	}
}

func TestStalledWorkRemainsPendingUntilACoordinatorArrives(t *testing.T) {
	f := newStalledWorkFixture(t, "idle", "")
	advanceStalledWorkFixture(t, f, true)
	f.checkIn(t, f.sender)
	r, err := f.e.query(f.ctx, func() core.Result {
		worker := f.e.state.Agents["worker"]
		prior := worker.StalledWorkNotice
		// Restart drops this app observation. The held episode must have its
		// own evidence to survive the transition.
		f.e.wakers.mu.Lock()
		delete(f.e.wakers.dibsTurn, worker.ID)
		f.e.wakers.mu.Unlock()
		return core.Result{"pending": prior != nil && prior.NoticeSerial == 0}
	})
	if err != nil || r["pending"] != true {
		t.Fatalf("no coordinator: expected a held observation: %v / %v", r, err)
	}
	got := f.checkIn(t, f.coordinator(t))
	if !hasStalledWorkMail(got) {
		t.Fatalf("arrival did not deliver held work: %v", got)
	}
}

func TestStalledWorkExcludesWaitingAndSteeredTurns(t *testing.T) {
	for _, tc := range []struct {
		name, mode, waiting string
	}{
		{"waiting", "idle", "build CI"},
		{"steered", "active", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStalledWorkFixture(t, tc.mode, tc.waiting)
			advanceStalledWorkFixture(t, f, tc.mode == "idle")
			got := f.checkIn(t, f.coordinator(t))
			if hasStalledWorkMail(got) {
				t.Fatalf("ineligible work produced a coordinator notice: %v", got)
			}
		})
	}
}

func TestStalledWorkUsesOnlyTheCurrentSession(t *testing.T) {
	f := newStalledWorkFixture(t, "idle", "")
	coordinator := f.coordinator(t)
	contactReplayDo(t, f.e, f.ctx, &core.Op{
		Kind: core.OpBindSession, Token: f.worker, SessionID: "2c3f0a11-2b44-4d90-9e57-1f2a3b4c5d6d",
	})
	advanceStalledWorkFixture(t, f, true)
	if got := f.checkIn(t, coordinator); hasStalledWorkMail(got) {
		t.Fatal("an offer to the previous session was used as evidence for the replacement")
	}
}

func TestStalledWorkUsesARealFinishingHook(t *testing.T) {
	f := newStalledWorkFixture(t, "idle", "")
	coordinator := f.coordinator(t)
	advanceStalledWorkFixture(t, f, true)
	if r, err := f.e.HookPoll(f.ctx, testcodexipc.Thread, "Stop", "", true, true); err != nil || r["error"] != nil {
		t.Fatalf("setup finishing hook: %v / %v", r, err)
	}
	_, err := f.e.query(f.ctx, func() core.Result {
		f.e.wakers.mu.Lock()
		defer f.e.wakers.mu.Unlock()
		wake := f.e.wakers.dibsTurn["worker"]
		if !wake.finishingHookAt.After(wake.offeredAt) {
			t.Error("setup: actual hook never reached the stall evidence")
		}
		// Put the finishing hook just after the now-stale offer. Rechecking
		// well after a healthy Stop must not reopen that old wake.
		wake.finishingHookAt = wake.offeredAt.Add(time.Second)
		f.e.wakers.dibsTurn["worker"] = wake
		f.e.hookAlive["worker"] = wake.finishingHookAt
		f.e.state.Agents["worker"].LastCoordination = wake.finishingHookAt
		f.e.seen["worker"] = wake.finishingHookAt
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.checkIn(t, coordinator); hasStalledWorkMail(got) {
		t.Fatal("a finishing hook covered the old wake")
	}
}

const stalledFixtureWork = "finish the approved compatibility patch"

type stalledWorkFixture struct {
	e              *Engine
	ctx            context.Context
	worker, sender string
}

func newStalledWorkFixture(t *testing.T, mode, waiting string) stalledWorkFixture {
	t.Helper()
	t.Setenv("DIBS_DIR", t.TempDir())
	app := testcodexipc.Start(t, mode, nil)
	e, ctx, worker, sender := nativeAppEngine(t)
	contactReplayDo(t, e, ctx, &core.Op{Kind: core.OpAckBoard, Token: worker})
	contactReplayDo(t, e, ctx, &core.Op{
		Kind: core.OpSetSlot, Token: worker, SlotID: "s1", Text: stalledFixtureWork, Waiting: waiting,
	})
	nativeAppSend(t, e, ctx, sender)
	awaitNative(t, app, 1)
	waitWakeDone(t, e, "worker")
	return stalledWorkFixture{e: e, ctx: ctx, worker: worker, sender: sender}
}

func (f stalledWorkFixture) checkIn(t *testing.T, token string) core.Result {
	t.Helper()
	r, err := f.e.Do(f.ctx, &core.Op{Kind: core.OpAckBoard, Token: token, MailboxPage: true})
	if err != nil || r["error"] != nil {
		t.Fatalf("check_in: %v / %v", r, err)
	}
	return r
}

func (f stalledWorkFixture) coordinator(t *testing.T) string {
	t.Helper()
	coordinator := registerAgent(t, f.e, "coordinator")
	if r, err := f.e.GrantRole(f.ctx, "coordinator", core.RoleCoordinator); err != nil || r["error"] != nil {
		t.Fatalf("setup coordinator: %v / %v", r, err)
	}
	return coordinator
}

// Shift only the fixture's timestamps. The declaration and wake use the real
// production entry points, and the board must show that the fixture is stalled.
func advanceStalledWorkFixture(t *testing.T, f stalledWorkFixture, offered bool) {
	t.Helper()
	e, ctx := f.e, f.ctx
	_, err := e.query(ctx, func() core.Result {
		past := time.Now().Add(-workingWithin - time.Minute)
		worker := e.state.Agents["worker"]
		worker.LastCoordination = past
		e.seen[worker.ID] = past
		e.contact[worker.ID] = contactEvidence{created: worker.CreatedSerial, at: past}
		e.declarationTimes[worker.ID]["s1"] = declarationTime{serial: worker.Slots["s1"].UpdatedSerial, at: past}
		e.wakers.mu.Lock()
		wake := e.wakers.dibsTurn[worker.ID]
		if wake.offeredAt.IsZero() == offered || !wake.finishingHookAt.IsZero() {
			t.Errorf("setup: native input offer = %v, want %v", !wake.offeredAt.IsZero(), offered)
		}
		if offered {
			wake.offeredAt = past
		}
		e.wakers.dibsTurn[worker.ID] = wake
		e.wakers.mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func hasStalledWorkMail(result core.Result) bool {
	mail := core.Result{"messages": result["messages"], "announcements": result["announcements"]}
	return strings.Contains(fmtResult(mail), stalledFixtureWork)
}
