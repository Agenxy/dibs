package main

import (
	"os"
	"strings"
	"testing"
)

// Trigger and concurrency declarations are static properties of the workflow,
// not a call-site count pretending to test a runtime condition.
func TestReleaseTriggerAndGlobalConcurrencyBoundary(t *testing.T) {
	b, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "concurrency:\n  group: release\n  cancel-in-progress: false\n") {
		t.Fatal("all versions must serialize in one uncancellable release group")
	}
	start, end := strings.Index(s, "\non:\n"), strings.Index(s, "\npermissions:\n")
	if start < 0 || end <= start {
		t.Fatal("workflow trigger boundary missing")
	}
	triggers := s[start:end]
	if !strings.Contains(triggers, "\n  workflow_dispatch:\n") || strings.Contains(triggers, "\n  push:") {
		t.Fatal("release is dispatch-only; a tag push must never publish")
	}
}
