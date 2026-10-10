// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Command acceptance is per original item, never a successful-delivery timer.
// Capture before execution; mail that arrives while it runs stays unoffered.
// Metadata-only acceptance records an FYI announcement, never full-body
// consumption or an outcome read. The command receipt itself stays derived.
func (e *Engine) freshCommandKeys(agent string, now time.Time) []string {
	var keys []string
	for _, key := range e.deliveryKeysAt(agent, now) {
		if !e.commandWritten[key] && !e.nativeUnknown[key] {
			keys = append(keys, key)
		}
	}
	return keys
}

func (e *Engine) recordCommandWritten(cmd wakePlan) {
	if len(cmd.commandKeys) == 0 {
		return
	}
	_, _ = e.query(e.wakeContext, func() core.Result {
		l := e.state.Agents[cmd.agent]
		if l == nil || l.Retired() || l.CreatedSerial != cmd.createdSerial || !l.SessionIsCurrent(cmd.session) ||
			e.commandEpoch[cmd.agent] != cmd.commandEpoch {
			return nil
		}
		if e.commandWritten == nil {
			e.commandWritten = map[string]bool{}
		}
		for _, key := range cmd.commandKeys {
			e.commandWritten[key] = true
			kind, item, _ := strings.Cut(key, ":")
			who, raw, _ := strings.Cut(item, "\x00")
			serial, _ := strconv.ParseUint(raw, 10, 64)
			m := e.state.Messages[serial]
			// The command may finish after a hook or authenticated read has
			// already presented this FYI. Its delayed metadata receipt adds
			// nothing then; do not rewrite coordination state on reconnect.
			if kind == "mail" && who == l.ID && m != nil && m.Type == core.MsgNotify && !e.notifyPresented(l.ID, m) {
				if err := e.announceFYI(l, serial, time.Now()); err != nil {
					return core.Result{"error": err}
				}
			}
		}
		return nil
	})
}

// An observed app incarnation is a new receiving endpoint. Forget only these
// outstanding delivery receipts, and fence an old in-flight command's success.
// Retrying the same incarnation keeps already recovered items deduplicated.
func (e *Engine) resetCommandIncarnation(agent, epoch string) {
	if e.commandEpoch[agent] == epoch {
		return // a retry of this observation keeps already recovered items deduplicated
	}
	if e.commandEpoch == nil {
		e.commandEpoch = map[string]string{}
	}
	e.commandEpoch[agent] = epoch
	for key := range e.commandWritten {
		_, item, _ := strings.Cut(key, ":")
		if strings.HasPrefix(item, agent+"\x00") {
			delete(e.commandWritten, key)
		}
	}
	for key := range e.nativeUnknown {
		_, item, _ := strings.Cut(key, ":")
		if strings.HasPrefix(item, agent+"\x00") {
			delete(e.nativeUnknown, key)
		}
	}
}

func (e *Engine) pruneCommandWritten(live map[string]bool) {
	for agent := range e.commandEpoch {
		if row := e.state.Agents[agent]; row == nil || row.Retired() {
			delete(e.commandEpoch, agent)
		}
	}
	for key := range e.commandWritten {
		if !live[key] {
			delete(e.commandWritten, key)
		}
	}
	for key := range e.nativeUnknown {
		if !live[key] {
			delete(e.nativeUnknown, key)
		}
	}
}
