// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestConfigureRefusesRetiredIdleDelayWithHintBeforeSaving(t *testing.T) {
	e, ctx, admin, _ := configureBoard(t)
	writes := 0
	e.SetSettingStore(func(_, _, _ string) error { writes++; return nil })
	_, err := e.Configure(ctx, admin, "wake.open_app_after_idle", "0s")
	var cerr *core.Error
	if !errors.As(err, &cerr) || !strings.Contains(cerr.Hint, "Remove the open_app_after_idle line") {
		t.Fatalf("retired setting lacked corrective hint: %v", err)
	}
	if writes != 0 {
		t.Fatal("refused setting reached the persistent writer")
	}
}
