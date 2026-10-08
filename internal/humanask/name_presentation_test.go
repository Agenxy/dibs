// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package humanask

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/notify"
)

func TestHumanNotificationsShowCurrentNameWithoutChangingSenderID(t *testing.T) {
	for _, kind := range []string{core.MsgRequest, core.MsgQuestion} {
		t.Run(kind, func(t *testing.T) {
			var title string
			m := Message{
				Type: kind, From: "old-id", FromName: "current-name\nspoofed line",
				Body: "body", Choices: []string{"yes"},
				ask: func(got, _ string, _ notify.Receipt, _ ...string) (string, error) {
					title = got
					return "", nil
				},
			}
			_, _ = Ask(m)
			if !strings.Contains(title, "current-name spoofed line") || strings.Contains(title, "old-id") ||
				strings.ContainsAny(title, "\r\n") {
				t.Fatalf("notification did not render the current name safely: %q", title)
			}
			if m.From != "old-id" {
				t.Fatal("presentation changed the stable sender id")
			}
		})
	}
}

func TestAdoptionApprovalTitleUsesCurrentNameWithoutChangingTargetID(t *testing.T) {
	var title string
	m := Message{
		Type: core.MsgRequest, From: "asker-id", FromName: "current-asker",
		Adopt: "old-target-id", AdoptName: "current-target",
		ask: func(got, _ string, _ notify.Receipt, _ ...string) (string, error) {
			title = got
			return "", nil
		},
	}
	_, _ = Ask(m)
	if !strings.Contains(title, "current-asker") || !strings.Contains(title, "current-target (formerly old-target-id)") ||
		strings.Contains(title, "asker-id") {
		t.Fatalf("adoption approval title omitted a current name: %q", title)
	}
	if m.Adopt != "old-target-id" {
		t.Fatal("presentation changed the stable adoption target")
	}
}
