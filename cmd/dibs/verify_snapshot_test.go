// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

// A ledger compacted at start begins at its snapshot's anchor. `dibs verify`
// and `dibs doctor` walk the chain themselves, and a walk that starts from the
// empty hash reported every compacted board as tampered with.
func TestVerifyAcceptsALedgerThatStartsAtItsSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(path, "n", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("n", core.DefaultLimits())
	old, recent := time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	for i, at := range []time.Time{old, old.Add(time.Minute), recent, recent.Add(time.Minute)} {
		op := &core.Op{Kind: core.OpRegister, Name: "agent" + string(rune('a'+i)), NewToken: "tok" + string(rune('a'+i))}
		if _, _, err := st.Apply(op, at); err != nil {
			t.Fatal(err)
		}
		if err := led.Append(st.Serial, at, op); err != nil {
			t.Fatal(err)
		}
	}
	_ = led.Close()
	full, err := verifyChain(path)
	if err != nil {
		t.Fatal("setup:", err)
	}

	led, err = ledger.Open(path, "n", box)
	if err != nil {
		t.Fatal(err)
	}
	led.KeepHistory(24 * time.Hour)
	if _, err := led.Replay(core.NewState("n", core.DefaultLimits())); err != nil {
		t.Fatal(err)
	}
	_ = led.Close()
	if _, ok, _ := ledger.SnapshotAnchor(path); !ok {
		t.Fatal("setup: the replay did not compact, so this proves nothing")
	}
	got, err := verifyChain(path)
	if err != nil || got.Head != full.Head || got.Lines != 2 {
		t.Fatalf("verify on a compacted ledger: %+v %v; want head %s over 2 kept lines", got, err, full.Head)
	}
	if err := os.Remove(filepath.Join(dir, ledger.SnapshotName)); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyChain(path); err == nil {
		t.Error("a compacted ledger without its snapshot verified")
	}
}
