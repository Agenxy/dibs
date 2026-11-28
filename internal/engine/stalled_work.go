// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

// The first coordinator check-in or a committed lifecycle event can observe
// stale work. A silent board acquires no new wake timer. Public board renders
// are reads, and the system notice records an observation, never a turn end.
func (e *Engine) observeStalledWork(now time.Time, observer, onlyWorker string) {
	now = now.Round(0)
	if onlyWorker != "" {
		e.observeStalledWorker(e.state.Agents[onlyWorker], observer, now)
		return
	}
	for _, id := range sortedAgentIDs(e.state) {
		if id != observer {
			e.observeStalledWorker(e.state.Agents[id], observer, now)
		}
	}
}

func (e *Engine) observeStalledWorker(worker *core.Agent, observer string, now time.Time) {
	work, fresh := e.stalledWorkCandidate(worker, now)
	if len(work.slots) == 0 {
		return
	}
	coordinator := e.stalledWorkCoordinator(worker.ID, observer)
	if coordinator != "" && e.reportStalledWork(worker, work, coordinator, now) {
		return
	}
	// Holding an observation does not mark it sent. A coordinator arrival
	// or a later check-in can deliver the same episode, even after replay.
	if fresh && (worker.StalledWorkNotice == nil || worker.StalledWorkNotice.DeclarationSerial < work.version) {
		e.holdStalledWork(worker, work.version, now)
	}
}

func (e *Engine) stalledWorkCandidate(worker *core.Agent, now time.Time) (stalledWork, bool) {
	if worker == nil || worker.Retired() || wakeHarness(worker) != "codex" {
		return stalledWork{}, false
	}
	e.wakers.mu.Lock()
	wake, turn := e.wakers.dibsTurn[worker.ID], e.boardTurnOf(worker)
	e.wakers.mu.Unlock()
	if turn.State == harnessenv.TurnInProgress && now.Sub(turn.ObservedAt) <= workingWithin {
		return stalledWork{}, false
	}
	prior := worker.StalledWorkNotice
	fresh := e.missingFinishForStalledWork(worker, wake, now)
	pending := e.pendingStalledWork(worker, wake, prior, now)
	if !fresh && !pending {
		return stalledWork{}, false
	}
	work := e.stalledWorkAt(worker, now)
	if prior != nil && !stalledWorkVersionEligible(prior, work.version, pending, fresh) {
		return stalledWork{}, false
	}
	return work, fresh
}

func stalledWorkVersionEligible(prior *core.StalledWorkNotice, version uint64, pending, fresh bool) bool {
	if version == prior.DeclarationSerial {
		return pending
	}
	return fresh && version > prior.DeclarationSerial
}

func (e *Engine) pendingStalledWork(
	worker *core.Agent, wake dibsTurnRecord, prior *core.StalledWorkNotice, now time.Time,
) bool {
	if prior == nil || prior.NoticeSerial != 0 ||
		prior.Session != wakeSessionOf(worker) || prior.Host != e.remoteHostOf(worker) ||
		wake.offeredAt.After(prior.ObservedAt) {
		return false
	}
	seen, _ := e.evidenceWithSource(worker, false) // startup grace is not work
	return !seen.After(prior.ObservedAt) && now.Sub(seen) > workingWithin &&
		wake.finishingHookAt.Before(prior.ObservedAt)
}

func (e *Engine) stalledWorkCoordinator(worker, observer string) string {
	human, reporter := e.humanRowLocked(), e.dibsRowLocked()
	if observer != worker && observer != human && observer != reporter {
		if row := e.state.Agents[observer]; row != nil && row.IsCoordinator() {
			return observer
		}
	}
	live, dormant := e.coordinatorsByLiveness(worker, human, reporter)
	if live != "" {
		return live
	}
	return dormant
}

func (e *Engine) missingFinishForStalledWork(worker *core.Agent, wake dibsTurnRecord, now time.Time) bool {
	if wake.session == "" || wake.created != worker.CreatedSerial || wake.session != wakeSessionOf(worker) ||
		wake.host != e.remoteHostOf(worker) ||
		wake.offeredAt.IsZero() || now.Sub(wake.offeredAt) < workingWithin ||
		now.Sub(e.lastEvidenceOf(worker)) <= workingWithin || !wake.finishingHookAt.Before(wake.offeredAt) {
		return false
	}
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	return e.wakers.dibsTurn[worker.ID] == wake
}

type stalledWork struct {
	version uint64
	slots   []core.Slot
	oldest  time.Time
}

func (e *Engine) stalledWorkAt(worker *core.Agent, now time.Time) stalledWork {
	var work stalledWork
	for _, slot := range openOf(e.workSlots(worker, now, false)) {
		work.version = max(work.version, slot.UpdatedSerial)
		if at := e.declarationChangedAt(worker, slot); !at.IsZero() && now.Sub(at) >= workingWithin {
			work.slots = append(work.slots, slot)
			if work.oldest.IsZero() || at.Before(work.oldest) {
				work.oldest = at
			}
		}
	}
	return work
}

func (e *Engine) holdStalledWork(worker *core.Agent, version uint64, now time.Time) {
	op := &core.Op{
		Kind: core.OpStalledWorkObserved, StalledWorker: worker.ID,
		SessionID: wakeSessionOf(worker), StalledWorkerHost: e.remoteHostOf(worker), DeclarationSerial: version,
	}
	if err := e.state.Admit(op); err == nil {
		_, _ = e.applyAndLedger(op, now)
	}
}

func (e *Engine) reportStalledWork(worker *core.Agent, work stalledWork, coordinator string, now time.Time) bool {
	reporter := e.state.Agents[e.dibsRowLocked()]
	if reporter == nil || reporter.Token == "" || reporter.Status != core.StatusActive {
		res, err := e.exec(dibsRegistration(), now)
		_, token, err := dibsRegistrationResult(res, err)
		if err != nil {
			slog.Warn("could not prepare the stalled work notice", "agent", worker.ID, "err", err)
			return false
		}
		reporter = e.state.AgentByToken(token)
	}

	op := &core.Op{
		Kind: core.OpStalledWorkNotified, AgentID: reporter.ID, Token: reporter.Token,
		To: coordinator, MsgType: core.MsgNotify, Body: stalledWorkBody(worker, work, now),
		StalledWorker: worker.ID, SessionID: wakeSessionOf(worker), StalledWorkerHost: e.remoteHostOf(worker),
		DeclarationSerial: work.version, MailboxCapacity: true,
	}
	err := e.state.Admit(op)
	if err == nil {
		_, err = e.applyAndLedger(op, now)
	}
	if err != nil {
		slog.Warn("could not send the stalled work notice", "agent", worker.ID, "coordinator", coordinator, "err", err)
	}
	return err == nil
}

func stalledWorkBody(worker *core.Agent, work stalledWork, now time.Time) string {
	var body strings.Builder
	fmt.Fprintf(&body, "Declared work for %q is stalled. No finishing hook was observed for this session.", worker.Name)
	for _, slot := range work.slots[:min(3, len(work.slots))] {
		text := trimRunes(squashSpace(slot.Text, 180), 180)
		fmt.Fprintf(&body, "\n%s: %q", slot.ID, text)
	}
	if len(work.slots) > 3 {
		fmt.Fprintf(&body, "\n%d more stalled items are listed on the board.", len(work.slots)-3)
	}
	fmt.Fprintf(&body, "\nThe oldest stalled declaration is unchanged for %s.", now.Sub(work.oldest).Truncate(time.Minute))
	body.WriteString("\nRun `dibs codex-hooks` on that machine to inspect its current hook status.")
	return body.String()
}

func (e *Engine) observeStalledWorkEvents(events []core.Event, now time.Time) {
	for _, event := range events {
		switch event.Type {
		case "agent.dormant", "agent.stale":
			e.observeStalledWork(now, "", event.Agent)
		case "agent.role_changed":
			if coordinator := e.state.Agents[event.Agent]; coordinator != nil && coordinator.IsCoordinator() {
				e.observeStalledWork(now, event.Agent, "")
			}
		}
	}
}
