// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDoctorReportsSocketBindingRefusalFromBoard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var board boardView
	if err := json.Unmarshal([]byte(`{"agents":[{"id":"worker","socket_wake":{"state":"refused","reason":"conflicting stated session bindings","hint":"bind_session from the real session, or update with the real cwd"}}]}`), &board); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	checkWakeRoutes(t.TempDir(), &board, hubHosts{}, func(string) {}, func(what, hint string) {
		warnings = append(warnings, what+" "+hint)
	})
	if got := strings.Join(warnings, "\n"); !strings.Contains(got, "worker") || !strings.Contains(got, "conflicting stated") || !strings.Contains(got, "bind_session") {
		t.Fatalf("doctor hides the daemon's refusal and corrective call: %s", got)
	}
}
