// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestLedgerReaderRefusesAnUnknownRequiredFeatureBeforeFold(t *testing.T) {
	l, path := newLedger(t)
	s := core.NewState("test", core.DefaultLimits())
	apply(t, s, l, &core.Op{Kind: core.OpRegister, Name: "author", NewToken: "token"}, t0)
	// A future writer knows this requirement. Current Admit must not admit it,
	// but replay checks reader support as a file-format property, not by
	// re-running today's admission policy against historical operations.
	apply(t, s, l, &core.Op{
		Kind: core.OpRequireV17Features, AgentID: "author",
		RequiredFeatures: []string{"future-reader-feature/v1"},
	}, t0)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	box, err := LoadOrCreateKey(filepath.Join(filepath.Dir(path), "key"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(path, "test", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	got := core.NewState("test", core.DefaultLimits())
	_, err = reader.Replay(got)
	if err == nil || !strings.Contains(err.Error(), "future-reader-feature/v1") ||
		!strings.Contains(err.Error(), "upgrade") || got.Serial != 1 || len(got.RequiredFeatures) != 0 {
		t.Fatalf("unknown requirement was folded or lacked upgrade guidance: serial=%d requirements=%v err=%v",
			got.Serial, got.RequiredFeatures, err)
	}
}
