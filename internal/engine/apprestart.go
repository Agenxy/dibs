// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"log/slog"
	"sort"
	"sync/atomic"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/wakeexec"
)

// SetAppRestartDefaults installs the operator's config, beneath the ledgered
// coordinator override. App restart delivery is independent of mail.
func (e *Engine) SetAppRestartDefaults(window, interval time.Duration, windowInFile, intervalInFile bool) {
	e.restartResumeDefault = window
	e.restartIntervalDefault = interval
	e.restartResumeFromFile = windowInFile
	e.restartIntervalFromFile = intervalInFile
}

func (e *Engine) appRestartSettings() (time.Duration, time.Duration) {
	window, interval := e.restartResumeDefault, e.restartIntervalDefault
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if s, ok := e.state.RestartSettings[core.RestartResumeSetting]; ok {
		window, _ = time.ParseDuration(s.Value) // admitted before ledger append
	}
	if s, ok := e.state.RestartSettings[core.RestartIntervalSetting]; ok {
		interval, _ = time.ParseDuration(s.Value)
	}
	return window, interval
}

// Two matching process observations reject transient ps races. Unknown and
// absent are never a transition to a new running incarnation. A daemon restart
// establishes a fresh baseline: delivery across daemon downtime is best effort.
type appEpochWatch struct {
	previous, sample string
	streak           int
	before           []restartCandidate
}

// Tests replace only this process-observation edge. Production always uses
// the bounded /bin/ps probe; the watcher and every delivery recheck still
// enter through the same path when an app epoch changes. Atomic replacement
// keeps test teardown safe while the watcher goroutine is winding down.
var appRestartEpoch = func() *atomic.Value {
	v := new(atomic.Value)
	v.Store(harnessenv.ChatGPTAppEpoch)
	return v
}()

func currentAppRestartEpoch() (string, bool) {
	return appRestartEpoch.Load().(func() (string, bool))()
}

func (e *Engine) watchAppRestarts(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var watch appEpochWatch
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		enabled, err := e.appRestartEnabled(ctx)
		if err != nil {
			return
		}
		if !enabled {
			watch = appEpochWatch{}
			continue // off means no process probe and no wake
		}
		epoch, known := currentAppRestartEpoch()
		if err := watch.observe(ctx, e, epoch, known); err != nil {
			slog.Warn("app-restart observation was not recorded", "err", err)
		}
	}
}

func (e *Engine) appRestartEnabled(ctx context.Context) (bool, error) {
	res, err := e.query(ctx, func() core.Result {
		window, _ := e.appRestartSettings()
		return core.Result{"enabled": window > 0}
	})
	return res["enabled"] == true, err
}

func (w *appEpochWatch) observe(ctx context.Context, e *Engine, epoch string, known bool) error {
	if !known || epoch == "absent" {
		w.sample, w.streak = "", 0
		return nil
	}
	if epoch != w.sample {
		w.sample, w.streak = epoch, 1
		return nil
	}
	w.streak++
	if w.streak < 2 {
		return nil
	}
	if epoch == w.previous {
		// Last known state WHILE the old app ran, never after replacement.
		_, err := e.query(ctx, func() core.Result {
			w.before = e.snapshotAppRestart(time.Now().UTC())
			return core.Result{"ok": true}
		})
		return err
	}
	_, err := e.query(ctx, func() core.Result {
		plans, interval, applyErr := e.observeAppRestart(w.previous, epoch, time.Now().UTC(), w.before)
		if applyErr != nil {
			return core.Result{"error": applyErr}
		}
		if len(plans) > 0 {
			go e.deliverAppRestart(ctx, epoch, plans, interval)
		}
		return core.Result{"ok": true}
	})
	if err == nil {
		w.previous, w.before = epoch, nil
	}
	return err
}

type restartCandidate struct {
	notice   core.RestartNotice
	plan     wakePlan
	activity time.Time
}

// The only test seam for restart queue admission. The integration guard enters
// sendRestartQueue and replaces the external queue command, not the shared
// app-open path that follows confirmed admission.
var restartQueue = wakeexec.RunRestartQueue

// snapshotAppRestart runs on the writer loop while the known app epoch still
// owns the process. It reads state only; no ledger entry on every tick.
func (e *Engine) snapshotAppRestart(now time.Time) []restartCandidate {
	window, _ := e.appRestartSettings()
	if window <= 0 {
		return nil
	}
	ids := make([]string, 0, len(e.state.Agents))
	for id := range e.state.Agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []restartCandidate
	for _, id := range ids {
		if candidate, ok := e.restartCandidateFor(e.state.Agents[id], now, window); ok {
			out = append(out, candidate)
		}
	}
	return out
}

func (e *Engine) restartCandidateFor(l *core.Agent, now time.Time, window time.Duration) (restartCandidate, bool) {
	if l == nil || l.Retired() || surfaceOf(l) != harnessenv.ChatGPTApp || e.remoteHostOf(l) != "" {
		return restartCandidate{}, false
	}
	thread := threadIDOf(l)
	if thread == "" {
		return restartCandidate{}, false
	}
	activity, source := e.lastEvidenceWithSource(l)
	if source == "boot_grace" {
		activity = l.LastCoordination
	}
	if activity.IsZero() || activity.After(now) || now.Sub(activity) > window {
		return restartCandidate{}, false
	}
	plan, ok := e.appRestartPlan(l, thread)
	if !ok {
		return restartCandidate{}, false
	}
	notice := core.RestartNotice{
		AgentID: l.ID, CreatedSerial: l.CreatedSerial,
		Slots: restartSlotRefs(l.Slots),
	}
	return restartCandidate{notice: notice, plan: plan, activity: activity}, true
}

func restartSlotRefs(slots map[string]core.Slot) []core.RestartSlotRef {
	slotIDs := make([]string, 0, len(slots))
	for id := range slots {
		slotIDs = append(slotIDs, id)
	}
	sort.Strings(slotIDs)
	refs := make([]core.RestartSlotRef, 0, len(slots))
	for _, id := range slotIDs {
		if slot := slots[id]; slot.Text != "" {
			refs = append(refs, core.RestartSlotRef{ID: id, UpdatedSerial: slot.UpdatedSerial})
		}
	}
	return refs
}

func (e *Engine) observeAppRestart(
	previous, epoch string, now time.Time, before []restartCandidate,
) ([]wakePlan, time.Duration, error) {
	window, interval := e.appRestartSettings()
	if window <= 0 {
		return nil, interval, nil
	}
	op := &core.Op{
		Kind: core.OpAppRestartObserved, RestartEpoch: epoch,
		RestartBaseline: previous == "", RestartObservedAt: now,
	}
	var plans []wakePlan
	if previous != "" && previous != epoch {
		for _, candidate := range before {
			if !e.eligibleRestartCandidate(candidate, now, window) {
				continue
			}
			op.RestartNotices = append(op.RestartNotices, candidate.notice)
			if len(candidate.plan.argv) > 0 {
				plans = append(plans, candidate.plan)
			}
		}
	}
	if err := e.state.Admit(op); err != nil {
		return nil, interval, err
	}
	if _, err := e.applyAndLedger(op, now); err != nil {
		return nil, interval, err
	}
	return plans, interval, nil
}

func (e *Engine) eligibleRestartCandidate(c restartCandidate, now time.Time, window time.Duration) bool {
	l := e.state.Agents[c.notice.AgentID]
	if l == nil || l.Retired() || l.CreatedSerial != c.notice.CreatedSerial {
		return false
	}
	if surfaceOf(l) != harnessenv.ChatGPTApp || e.remoteHostOf(l) != "" {
		return false
	}
	if c.activity.After(now) || now.Sub(c.activity) > window {
		return false
	}
	return c.plan.thread != "" && threadIDOf(l) == c.plan.thread
}

func (e *Engine) appRestartPlan(l *core.Agent, thread string) (wakePlan, bool) {
	e.wakers.mu.Lock()
	defer e.wakers.mu.Unlock()
	cmd := e.commandFor(l)
	// Nothing arbitrary, no fallback command, no remote bridge, no socket.
	if !wakeexec.NativeQueueTemplate(cmd.argv) {
		return wakePlan{}, false
	}
	if e.wakeStillQueuedLocked(l, time.Now()) {
		return wakePlan{thread: thread}, true // existing queued notice will trigger the read
	}
	f := wakeexec.Fields{
		Thread: thread, Agent: l.ID, MsgType: wakeexec.KindAppRestart,
		Message: wakeexec.Compose(wakeexec.KindAppRestart),
	}
	return wakePlan{
		argv: f.Apply(cmd.argv), thread: thread, agent: l.ID, cwd: cwdOf(l),
		surface: surfaceOf(l), harness: wakeHarness(l),
	}, true
}

func (e *Engine) deliverAppRestart(ctx context.Context, epoch string, plans []wakePlan, interval time.Duration) {
	for i, plan := range plans {
		if i > 0 {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		current, known := currentAppRestartEpoch()
		if !known || current != epoch {
			return // a later app incarnation owns any further sweep
		}
		if ok := e.sendRestartQueue(ctx, epoch, plan); ok {
			e.noteQueuedWake(plan.agent, time.Now())
		}
	}
}

func (e *Engine) sendRestartQueue(ctx context.Context, epoch string, plan wakePlan) bool {
	out := restartQueue(plan.argv, plan.agent, plan.cwd)
	if out.Retryable {
		// The lock was never acquired, so no command ran. One retry is safe;
		// nonzero command exits are ambiguous and are never retried blindly.
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		current, known := currentAppRestartEpoch()
		if !known || current != epoch {
			return false
		}
		out = restartQueue(plan.argv, plan.agent, plan.cwd)
	}
	if out.Retryable {
		slog.Warn("app-restart queue admission remained contended after one retry", "agent", plan.agent)
		return false
	}
	e.noteCommandOutcome(plan.agent, plan.argv, out.OK)
	if !out.OK {
		slog.Warn("app-restart queue command did not confirm admission; not retrying ambiguous outcome",
			"agent", plan.agent)
		return false
	}
	e.showInApp(plan, plan.agent)
	return true
}

// Called only on the writer loop, from the two token-authenticated read doors.
func (e *Engine) readAppRestart(token string, now time.Time) (string, error) {
	res, err := e.applyAndLedger(&core.Op{Kind: core.OpReadAppRestart, Token: token}, now)
	if err != nil {
		return "", err
	}
	text, _ := res["notice"].(string)
	return text, nil
}
