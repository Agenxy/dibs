package main

import (
	"errors"
	"maps"
	"strings"
)

const proofWorkflow = ".github/workflows/release-proof-check.yml"

// These are static authority boundaries, not a claim that a hosted token works.
// The actual protected-main job must still enter the production validator.
func proofWorkflowBoundary(proof, finalizer string) error {
	for _, required := range []string{
		"name: release proof check\n", "\n  workflow_dispatch:\n",
		"\npermissions:\n  contents: read\n\n",
		"\n    permissions:\n      contents: read\n    steps:\n",
		"\n    if: github.ref == 'refs/heads/main'\n",
		"\n        run: go run ./tools/releaseflow -phase validate\n",
		"ref: ${{ github.sha }}", "persist-credentials: false",
	} {
		if !strings.Contains(proof, required) {
			return errors.New("read-only proof workflow boundary missing: " + required)
		}
	}
	for _, forbidden := range []string{
		"secrets.", "id-token:", "actions:", "\n  push:", "\n  workflow_run:",
		"\n  workflow_call:", "-phase preflight", "-phase publish", "git push", "upload-artifact",
	} {
		if strings.Contains(proof, forbidden) {
			return errors.New("proof workflow widens authority: " + forbidden)
		}
	}
	if err := proofWorkflowShape(proof); err != nil {
		return err
	}
	if !strings.HasPrefix(finalizer, "name: release finalizer\n") ||
		!strings.Contains(finalizer, "\n    workflows: [release]\n") ||
		strings.Count(finalizer, "workflows:") != 1 {
		return errors.New("finalizer must name only the exact release workflow, never release proof check")
	}
	return nil
}

func proofWorkflowShape(proof string) error {
	if strings.Count(proof, "permissions:") != 2 || strings.Count(proof, "\n        run:") != 1 {
		return errors.New("proof workflow must have only its two read grants and one validator command")
	}
	start, end := strings.Index(proof, "\non:\n"), strings.Index(proof, "\npermissions:\n")
	jobs := strings.Index(proof, "\njobs:\n")
	if start < 0 || end <= start || jobs <= end ||
		len(workflowJobStart.FindAllStringIndex(proof[start:end], -1)) != 1 ||
		len(workflowJobStart.FindAllStringIndex(proof[jobs:], -1)) != 1 {
		return errors.New("proof workflow must declare only dispatch and the single proof-check job")
	}
	return nil
}

func proofWorkflowParity(release, proof string) error {
	prod, err := workflowSetupPins(release, "preflight")
	if err != nil {
		return err
	}
	want := map[string]string{"runner": prod["runner"]}
	for key, value := range prod {
		for _, action := range []string{"actions/checkout", "jdx/mise-action", "sigstore/cosign-installer"} {
			if key == "action/"+action || strings.HasPrefix(key, "input/"+action+"/") {
				want[key] = value
			}
		}
	}
	got, err := workflowSetupPins(proof, "proof-check")
	if err != nil {
		return err
	}
	if !maps.Equal(want, got) {
		return errors.New("proof-check differs from production validation toolchain pins/inputs")
	}
	return nil
}
