// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package procinfo

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSnapshotProcessNamesStopAtFirstNUL(t *testing.T) {
	raw, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid())
	if err != nil {
		t.Fatal("setup kernel identity:", err)
	}
	want, _, _ := strings.Cut(string(raw.Proc.P_comm[:]), "\x00")
	if want == "" {
		t.Fatal("setup: own kernel command name is empty")
	}
	rows, err := Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if strings.ContainsRune(row.Comm, 0) {
			t.Errorf("pid %d retained bytes after its C command name: %q", row.PID, row.Comm)
		}
		if row.PID == os.Getpid() {
			found = true
			if row.Comm != want {
				t.Errorf("own snapshot name %q, kernel name %q", row.Comm, want)
			}
		}
	}
	if !found {
		t.Fatal("snapshot omitted the live test process")
	}
}
