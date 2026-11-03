// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDoctorReportsAnArchivedBindingEvenWithoutAnOrdinaryBoardRow(t *testing.T) {
	var board boardView
	if err := json.Unmarshal([]byte(`{"agents":[],"app_binding_alerts":[{"agent":"old-agent","reason":"archived","hint":"Mail remains stored; reattach the same agent from an existing app thread."}]}`), &board); err != nil {
		t.Fatal(err)
	}
	var reports []string
	checkWakeRoutes(t.TempDir(), &board, hubHosts{}, func(string) {}, func(message, hint string) { reports = append(reports, message+" "+hint) })
	var matching []string
	for _, report := range reports {
		if strings.Contains(report, "old-agent") {
			matching = append(matching, report)
		}
	}
	if len(matching) != 1 || !strings.Contains(matching[0], "archived thread binding") || !strings.Contains(matching[0], "Mail remains stored") {
		t.Fatalf("doctor lost archived binding evidence: %v", reports)
	}
}
