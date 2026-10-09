// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestSettingsRefusesRetiredCooldownWithCorrectiveHint(t *testing.T) {
	e, ctx, admin, _ := configureBoard(t)
	_, err := e.Configure(ctx, admin, "wake.exec.codex.cooldown", "90s")
	var got *core.Error
	if !errors.As(err, &got) || got.Code != "E_NO_SETTING" ||
		!strings.Contains(got.Hint, "Remove the cooldown line") || !strings.Contains(got.Msg, "removed") {
		t.Fatalf("retired setting did not give its specific repair: %v", err)
	}
}
