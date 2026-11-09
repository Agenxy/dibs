// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"os"
	"regexp"
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
	if err := candidateArtifactBoundary(candidate); err != nil {
		return err
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

// Artifact selection is a static dependency edge. A failed-only rerun keeps
// the producer's outputs but increments the consumer's run_attempt, so neither
// a second spelling of the name nor a consumer-local attempt is authoritative.
func candidateArtifactBoundary(workflow string) error {
	job := func(name string) (string, error) {
		marker := "\n  " + name + ":\n"
		if strings.Count(workflow, marker) != 1 {
			return "", errors.New("candidate artifact job missing or ambiguous: " + name)
		}
		body := strings.SplitN(workflow, marker, 2)[1]
		if next := regexp.MustCompile(`(?m)^  [^\s].*:\s*$`).FindStringIndex(body); next != nil {
			body = body[:next[0]]
		}
		return body, nil
	}
	action := func(job, name string) (string, error) {
		marker := "      - uses: actions/" + name + "@"
		if strings.Count(job, marker) != 1 {
			return "", errors.New("candidate artifact action missing or ambiguous: " + name)
		}
		body := strings.SplitN(job, marker, 2)[1]
		if end := strings.Index(body, "\n      - "); end >= 0 {
			body = body[:end]
		}
		return body, nil
	}
	producer, err := job("artifacts")
	if err != nil {
		return err
	}
	for _, required := range []string{
		"    env:\n      CANDIDATE_ARTIFACT_NAME: candidate-install-${{ github.run_id }}-${{ github.run_attempt }}\n",
		"      artifact_name: ${{ env.CANDIDATE_ARTIFACT_NAME }}\n",
	} {
		if !strings.Contains(producer, required) {
			return errors.New("candidate artifact producer must own and export its upload name: " + required)
		}
	}
	upload, err := action(producer, "upload-artifact")
	if err != nil {
		return err
	}
	consumer, err := job("linux")
	if err != nil {
		return err
	}
	download, err := action(consumer, "download-artifact")
	if err != nil {
		return err
	}
	if !strings.Contains(upload, "          name: ${{ env.CANDIDATE_ARTIFACT_NAME }}\n") ||
		!strings.Contains(download, "          name: ${{ needs.artifacts.outputs.artifact_name }}\n") {
		return errors.New("candidate download must select the producer's exported upload name")
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
	if !strings.Contains(published, "-tag published ") ||
		strings.Contains(published, "continue-on-error:") || strings.Contains(published, "candidate-install") {
		t.Fatal("published-release finding was pinned, hidden or combined with candidate acceptance")
	}
}

func TestCandidateArtifactNameBelongsToTheProducerAcrossReruns(t *testing.T) {
	body, err := os.ReadFile("../../.github/workflows/candidate-install.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	if err := candidateArtifactBoundary(workflow); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ name, from, to string }{
		{"producer name", "CANDIDATE_ARTIFACT_NAME: candidate-install-${{ github.run_id }}-${{ github.run_attempt }}", "CANDIDATE_ARTIFACT_NAME: candidate-install"},
		{"producer output", "artifact_name: ${{ env.CANDIDATE_ARTIFACT_NAME }}", "artifact_name: missing"},
		{"upload input", "name: ${{ env.CANDIDATE_ARTIFACT_NAME }}", "name: candidate-install-${{ github.run_id }}-${{ github.run_attempt }}"},
		{"consumer attempt", "name: ${{ needs.artifacts.outputs.artifact_name }}", "name: candidate-install-${{ github.run_id }}-${{ github.run_attempt }}"},
	} {
		changed := strings.ReplaceAll(workflow, mutation.from, mutation.to)
		if changed == workflow || candidateArtifactBoundary(changed) == nil {
			t.Fatalf("artifact ownership guard missed %s", mutation.name)
		}
	}
	// A matching name elsewhere cannot disguise the wrong download input.
	changed := strings.ReplaceAll(workflow, "name: ${{ needs.artifacts.outputs.artifact_name }}", "name: candidate-install-${{ github.run_id }}-${{ github.run_attempt }}")
	changed += "\n# name: ${{ needs.artifacts.outputs.artifact_name }}\n"
	if candidateArtifactBoundary(changed) == nil {
		t.Fatal("a decoy outside the download step satisfied the artifact ownership guard")
	}
}
