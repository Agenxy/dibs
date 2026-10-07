// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package humanask

import (
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/notify"
)

func TestAskDoesNotHidePresenterFailures(t *testing.T) {
	want := errors.New("notifier crashed before posting")
	for _, kind := range []string{core.MsgRequest, core.MsgQuestion} {
		t.Run(kind, func(t *testing.T) {
			m := Message{Type: kind, Choices: []string{"yes"}, ask: func(string, string, notify.Receipt, ...string) (string, error) { return "", want }}
			a, err := Ask(m)
			if !errors.Is(err, want) || a.Disposition != "" {
				t.Fatalf("failure became %v, %v", a, err)
			}
		})
	}
}
