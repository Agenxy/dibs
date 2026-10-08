// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"fmt"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

const queueNoticeSuffix = ". It may already be handled."

// queueNoticeAt changes only Dibs' own text on the canonical native queue
// route. Issued means this local admission attempt, not app acceptance or a
// claim that mail is still unread when a durable queue item finally arrives.
func queueNoticeAt(argv []string, at time.Time) []string {
	if !nativeQueueRoute(argv) {
		return argv
	}
	kind, ok := legacyWakeKind(argv[5])
	if !ok {
		return argv
	}
	if kind == "" || kind == "notice" {
		kind = "coordination"
	}
	out := append([]string(nil), argv...)
	out[5] = fmt.Sprintf("Dibs: %s notice issued at %s%s",
		kind, at.UTC().Truncate(time.Second).Format(time.RFC3339), queueNoticeSuffix)
	return out
}

// Historical text remains a wire format: dormant writers may still emit it.
// The event vocabulary, not participant text, decides the variable kind.
func legacyWakeKind(text string) (string, bool) {
	kinds := []string{
		"", core.MsgNotify, core.MsgQuestion, core.MsgRequest, core.MsgHandoff,
		"notice", KindContinuation, KindRecheck, KindAppRestart,
	}
	for _, kind := range kinds {
		if text == Compose(kind) {
			return kind, true
		}
	}
	kind, ok := strings.CutPrefix(text, "Dibs: a new ")
	if !ok {
		return "", false
	}
	kind, ok = strings.CutSuffix(kind, " is waiting.")
	return kind, ok && text == Compose(kind) && core.IsMailEvent("message."+kind)
}

func timestampedWake(text string) bool {
	_, known := noticeIssuedAt(text)
	return known
}

func noticeIssuedAt(text string) (time.Time, bool) {
	body, ok := strings.CutPrefix(text, "Dibs: ")
	if !ok {
		return time.Time{}, false
	}
	kind, stamp, ok := strings.Cut(body, " notice issued at ")
	if !ok {
		return time.Time{}, false
	}
	stamp, ok = strings.CutSuffix(stamp, queueNoticeSuffix)
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil || stamp != at.UTC().Truncate(time.Second).Format(time.RFC3339) {
		return time.Time{}, false
	}
	if kind == "coordination" {
		return at, true
	}
	got, known := legacyWakeKind(Compose(kind))
	if known && got == kind && kind != "" && kind != "notice" {
		return at, true
	}
	return time.Time{}, false
}
