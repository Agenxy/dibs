package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/wakeexec"
)

const maxAppReconnects = 128

type reconnectTarget struct {
	agent   string
	created uint64
	thread  string
}

// AppReconnected observes the running harness; it does not create a session,
// refresh activity, consume mail, or change replayable state. Startup discovery
// can therefore repair delivery before the model has made its first call.
func (e *Engine) AppReconnected(ctx context.Context, host string, app harnessenv.AppIncarnation) (err error) {
	if host == "" || host != e.HostID() || app.PID <= 1 || app.Start == "" {
		return nil
	}
	key := appReconnectKey(host, app)
	defer func() {
		if err != nil {
			// A cancelled startup or failed receipt write is retryable. The
			// same generation leaves already recovered queue items coalesced.
			retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			_, _ = e.query(retryCtx, func() core.Result {
				delete(e.appReconnects, key)
				return nil
			})
		}
	}()
	at := time.Now()
	res, err := e.query(ctx, func() core.Result {
		return core.Result{"targets": e.appReconnectTargets(host, app, at)}
	})
	if err != nil {
		return err
	}
	targets, _ := res["targets"].([]reconnectTarget)
	for _, target := range targets {
		// A process incarnation, rather than clock ordering, invalidates the
		// inferred receipt. Disk I/O stays outside the writer.
		if err = wakeexec.NoteQueueReconnect(target.thread, key); err != nil {
			return err
		}
		_, err = e.query(ctx, func() core.Result {
			agent := e.state.Agents[target.agent]
			if agent.Retired() || agent.CreatedSerial != target.created || threadIDOf(agent) != target.thread {
				delete(e.reconnectMail, target.agent)
				return nil
			}
			e.noteArrivalDuringWake(target.agent)
			e.retryWakeDecision(target.agent)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Runs on the writer; all process probes and receipt I/O are outside it.
func (e *Engine) appReconnectTargets(host string, app harnessenv.AppIncarnation, at time.Time) []reconnectTarget {
	key := appReconnectKey(host, app)
	if _, seen := e.appReconnects[key]; seen {
		return nil
	}
	e.rememberAppReconnect(key, at)
	var targets []reconnectTarget
	for id, agent := range e.state.Agents {
		if agent.Retired() || surfaceOf(agent) != harnessenv.ChatGPTApp ||
			e.remoteHostOf(agent) != "" || !e.hasReconnectMail(id) {
			continue
		}
		thread := threadIDOf(agent)
		if thread == "" {
			continue
		}
		if e.reconnectMail == nil {
			e.reconnectMail = map[string]uint64{}
		}
		e.reconnectMail[id] = agent.CreatedSerial
		e.wakers.mu.Lock()
		delete(e.wakers.queued, id)
		delete(e.wakers.queuedPrompt, id)
		e.wakers.mu.Unlock()
		targets = append(targets, reconnectTarget{id, agent.CreatedSerial, thread})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].agent < targets[j].agent })
	return targets
}

func appReconnectKey(host string, app harnessenv.AppIncarnation) string {
	return fmt.Sprintf("%s\x00%d@%s", host, app.PID, app.Start)
}

func (e *Engine) rememberAppReconnect(key string, at time.Time) {
	if e.appReconnects == nil {
		e.appReconnects = map[string]time.Time{}
	}
	if len(e.appReconnects) >= maxAppReconnects {
		var oldest string
		for k, when := range e.appReconnects {
			if oldest == "" || when.Before(e.appReconnects[oldest]) {
				oldest = k
			}
		}
		delete(e.appReconnects, oldest)
	}
	e.appReconnects[key] = at
}

// Reconnect shares actionable classification with socket wakes. Informational
// updates remain available at the next hook; an app restart is not a reason to
// spend a turn on them. This check uses current waiting declarations for DONE.
func (e *Engine) hasReconnectMail(agent string) bool {
	return e.actionableSocketMail(e.state.Agents[agent], time.Now(), false)
}

func (e *Engine) hasRetryMail(agent string) bool {
	if e.actionableSocketMail(e.state.Agents[agent], time.Now(), true) {
		return true
	}
	row := e.state.Agents[agent]
	created, reconnecting := e.reconnectMail[agent]
	if !row.Retired() && reconnecting && created == row.CreatedSerial && e.hasReconnectMail(agent) {
		return true
	}
	delete(e.reconnectMail, agent)
	return false
}
