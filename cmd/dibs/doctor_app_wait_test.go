// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"strings"
	"testing"
)

func TestDoctorExplainsTheEndedAppSessionWait(t *testing.T) {
	b := &boardView{AppBindingAlerts: []appBindingDiagnostic{{Agent: "worker", Reason: "waiting-thread-open", Hint: "Mail remains stored until its next ordinary session start."}}}
	var reports []string
	checkAppBindings(b, func(message, hint string) { reports = append(reports, message+" "+hint) })
	if len(reports) != 1 || !strings.Contains(reports[0], "waiting for its thread to be opened") || !strings.Contains(reports[0], "Mail remains stored") {
		t.Fatalf("doctor omitted the actionable wait: %v", reports)
	}
}
