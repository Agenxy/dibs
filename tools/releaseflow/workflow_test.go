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

func TestProofCheckUsesTheReadOnlyProductionDoorAndCannotTriggerFinalizer(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		body, err := os.ReadFile("../../" + path)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	proof, finalizer, release := read(proofWorkflow), read(".github/workflows/release-finalize.yml"), read(workflowPath)
	if err := proofWorkflowBoundary(proof, finalizer); err != nil {
		t.Fatal(err)
	}
	if err := proofWorkflowParity(release, proof); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ from, to string }{
		{"name: release proof check", "name: release"},
		{"contents: read", "contents: write"},
		{"-phase validate", "-phase preflight"},
		{"refs/heads/main", "refs/heads/other"},
		{"if: github.ref == 'refs/heads/main'", "if: github.ref == 'refs/heads/main' || true"},
		{"persist-credentials: false", "persist-credentials: true"},
		{"on:\n", "on:\n  push:\n"},
		{"jobs:\n", "jobs:\n  extra:\n    permissions: {contents: write}\n"},
	} {
		changed := strings.ReplaceAll(proof, mutation.from, mutation.to)
		if changed == proof || proofWorkflowBoundary(changed, finalizer) == nil {
			t.Fatalf("guard missed authority mutation: %s", mutation.from)
		}
	}
	changed := strings.ReplaceAll(finalizer, "workflows: [release]", "workflows: [release, release proof check]")
	if changed == finalizer || proofWorkflowBoundary(proof, changed) == nil {
		t.Fatal("proof completion was allowed to trigger finalizer")
	}
	for _, pin := range []string{
		"cosign-release: v3.1.3", "version: 2026.7.7", "experimental: true",
		"checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
	} {
		if proofWorkflowParity(release, strings.ReplaceAll(proof, pin, pin+"-changed")) == nil {
			t.Fatalf("guard missed proof toolchain drift: %s", pin)
		}
	}
}
