// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// Workflow authority and dependency edges are static properties. Actual
// Homebrew installation remains a separate hosted proof, never inferred here.
func candidateWorkflowBoundary(candidate, release string) error {
	for _, forbidden := range []string{"secrets.", "contents: write", "id-token:", "continue-on-error", "secrets: inherit"} {
		if strings.Contains(candidate, forbidden) {
			return errors.New("candidate workflow widens authority or hides failure: " + forbidden)
		}
	}
	for _, required := range []string{
		"\npermissions:\n  contents: read\n", "\n  workflow_call:\n", "runs-on: macos-latest",
		"runs-on: ubuntu-latest", "ref: ${{ inputs.sha }}", "persist-credentials: false",
		"needs: artifacts", "-mode candidate-build", "-mode candidate-linux", "if: always()",
		"/dibs-candidate/candidate.json", "/dibs-candidate/dibs.rb", "/dibs-candidate/checksums.txt",
		"/dibs-candidate/*.tar.gz", "/candidate-linux-receipt/receipt.json", "/candidate-linux-receipt/*.log",
	} {
		if !strings.Contains(candidate, required) {
			return errors.New("candidate workflow boundary missing: " + required)
		}
	}
	for _, required := range []string{
		"candidate-source:\n    if: inputs.mode == 'preflight' && github.ref == 'refs/heads/main'",
		"candidate-install:\n    needs: candidate-source\n    uses: ./.github/workflows/candidate-install.yml",
		"preflight:\n    needs: candidate-install\n", "commit-tag:\n    needs: preflight\n",
	} {
		if !strings.Contains(release, required) {
			return errors.New("candidate acceptance no longer gates release preflight/tag push: " + required)
		}
	}
	return nil
}

func TestCandidateAcceptanceIsReadOnlyAndRequiredBeforeReleasePreflight(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile("../../.github/workflows/" + path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	candidate, release := read("candidate-install.yml"), read("release.yml")
	if err := candidateWorkflowBoundary(candidate, release); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ from, to string }{
		{"contents: read", "contents: write"},
		{"persist-credentials: false", "persist-credentials: true"},
		{"runs-on: macos-latest", "runs-on: ubuntu-latest"},
		{"needs: artifacts", "needs: []"},
		{"if: always()", "if: success()"},
		{"permissions:\n", "permissions:\n  id-token: write\n"},
		{"/dibs-candidate/*.tar.gz", "/dibs-candidate/**"},
	} {
		changed := strings.ReplaceAll(candidate, mutation.from, mutation.to)
		if changed == candidate || candidateWorkflowBoundary(changed, release) == nil {
			t.Fatalf("candidate authority guard missed %q", mutation.from)
		}
	}
	for _, edge := range []string{"needs: candidate-source", "needs: candidate-install", "needs: preflight"} {
		changed := strings.ReplaceAll(release, edge, "needs: []")
		if changed == release || candidateWorkflowBoundary(candidate, changed) == nil {
			t.Fatalf("release bypassed acceptance by removing %s", edge)
		}
	}
	published := read("public-install-check.yml")
	if !strings.Contains(published, "-tag v0.0.16") || strings.Contains(published, "continue-on-error:") ||
		strings.Contains(published, "candidate-install") {
		t.Fatal("published v0.0.16 finding was hidden or combined with candidate acceptance")
	}
}
