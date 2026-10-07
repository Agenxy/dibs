// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

func TestReplayFailureHintNamesTheLedgerFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node_id"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.jsonl")
	led, err := ledger.Open(path, "test", box)
	if err != nil {
		t.Fatal(err)
	}
	if err := led.Append(1, t0, &core.Op{Kind: core.OpRegister, Name: "probe", NewToken: "probe"}); err != nil {
		t.Fatal(err)
	}
	if err := led.Append(2, t0, &core.Op{Kind: "invalid_probe_op", AgentID: "probe"}); err != nil {
		t.Fatal(err)
	}
	if err := led.Close(); err != nil {
		t.Fatal(err)
	}
	err = checkReplay(dir, Config{Addr: "127.0.0.1:4777", InsecurePlaintext: true}, "")
	if err == nil || !strings.Contains(err.Error(), "E_BAD_OP") {
		t.Fatalf("setup: expected semantic replay failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "dibs verify "+path) {
		t.Fatalf("hint points verify at a directory, not the ledger: %v", err)
	}
}
