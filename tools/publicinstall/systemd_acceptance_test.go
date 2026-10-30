// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"strings"
	"testing"
)

func TestSystemdProcessEvidenceRequiresActiveEpochAndUnit(t *testing.T) {
	valid := "MainPID=1234\nActiveState=active\nExecMainStartTimestampMonotonic=984321\nFragmentPath=/fixture/dibs.service\n"
	state, err := parseSystemdState(valid)
	if err != nil || state.PID != 1234 || state.Start != "984321" || state.Unit != "/fixture/dibs.service" {
		t.Fatalf("valid process evidence refused: %+v %v", state, err)
	}
	for _, bad := range []string{
		strings.Replace(valid, "MainPID=1234", "MainPID=0", 1),
		strings.Replace(valid, "ActiveState=active", "ActiveState=inactive", 1),
		strings.Replace(valid, "Monotonic=984321", "Monotonic=0", 1),
		strings.Replace(valid, "/fixture/dibs.service", "dibs.service", 1),
		valid + "MainPID=9999\n", "not-properties",
	} {
		if _, err := parseSystemdState(bad); err == nil {
			t.Fatalf("incomplete/ambiguous service evidence accepted: %q", bad)
		}
	}
}
