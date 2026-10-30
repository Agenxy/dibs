// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The published archive includes the Mac helper even on Linux. Drive the
// actual doctor command beside an executable with its Mach-O magic; existence
// and mode were the only evidence Available used, so Linux offered Touch ID.
func TestLinuxDoctorWithArchivedMacHelperAtHome(t *testing.T) {
	f := newSystemdInstallFixture(t)
	helper := filepath.Join(filepath.Dir(f.cli), "dibs-presence")
	if err := os.WriteFile(helper, []byte{0xcf, 0xfa, 0xed, 0xfe}, 0o700); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(helper); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("setup: helper is not executable: %v", err)
	}
	for name, body := range map[string]string{
		"local.secret": "private-doctor-fixture\n", "node_id": "fixture-node\n", "ledger.jsonl": "",
	} {
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := f.command(t.Context(), "doctor").CombinedOutput()
	if err == nil {
		t.Fatal("setup: unreachable fixture board was accepted")
	}
	text := string(out)
	if !strings.Contains(text, "ledger chain intact") {
		t.Fatalf("setup: doctor did not reach the local board/presence check: %s", out)
	}
	if strings.Contains(text, "`dibs web` unlocks the board with Touch ID") {
		t.Errorf("Linux offers the Mac-only biometric path: %s", out)
	}
	if !strings.Contains(text, "dibs admin set-password") {
		t.Errorf("Linux does not name the usable password setup: %s", out)
	}
	if !strings.Contains(text, "data dir ~/.dibs") || strings.Contains(text, "data dir ./.dibs") {
		t.Errorf("HOME=cwd did not use the home spelling: %s", out)
	}
}
