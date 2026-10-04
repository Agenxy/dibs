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

func TestRehearsalWorkflowHasClosedInputsAndNoProductionSecretsOrDownstreamWrites(t *testing.T) {
	b, err := os.ReadFile("../../" + publicationWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, forbidden := range []string{"secrets.", "repository:", "certificate_identity:", "registry-publish", "./tools/registrypublish", "./tools/stampserver", "git push", "\n  push:", "actions/upload-artifact"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("scratch workflow widens authority: %s", forbidden)
		}
	}
	for _, required := range []string{"DIBS_RELEASE_TARGET: rehearsal", "-phase full-publication-validate", "-phase full-publication", "cancel-in-progress: false"} {
		if !strings.Contains(s, required) {
			t.Fatalf("scratch workflow missing %s", required)
		}
	}
	if strings.Index(s, "-phase full-publication-validate") >= strings.Index(s, "uses: sigstore/cosign-installer") {
		t.Fatal("closed source validation must precede signing setup")
	}
}
