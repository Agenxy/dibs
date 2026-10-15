// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Mail first shown long after it was sent says so, on the page and in every
// later read, because it was delivered late: k7-dev triaged eight-day-old
// notifies as fresh (2026-10-14).
func TestMailDeliveredLongAfterItWasSentSaysSo(t *testing.T) {
	sent := time.Date(2026, 10, 6, 9, 36, 0, 0, time.UTC)
	now := sent.Add(8*24*time.Hour + time.Hour)
	pending := &core.Message{State: core.MsgStatePending, SentAt: sent}
	if note := lateNote(pending, now); !strings.Contains(note, "8 days") {
		t.Fatalf("pending mail shown eight days late: %q", note)
	}
	delivered := &core.Message{State: core.MsgStateDelivered, SentAt: sent, DeliveredTime: now}
	if note := lateNote(delivered, now.Add(time.Hour)); !strings.Contains(note, "8 days") {
		t.Fatalf("the note must survive delivery: %q", note)
	}
	prompt := &core.Message{State: core.MsgStateDelivered, SentAt: sent, DeliveredTime: sent.Add(time.Minute)}
	if note := lateNote(prompt, now); note != "" {
		t.Fatalf("mail delivered promptly read later is not late: %q", note)
	}
}
