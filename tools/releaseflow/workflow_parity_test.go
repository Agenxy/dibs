package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

var (
	workflowJobStart = regexp.MustCompile(`(?m)^  [A-Za-z0-9_-]+:\s*$`)
	miseVersionPin   = regexp.MustCompile(`^[0-9]{4}\.[0-9]{1,2}\.[0-9]{1,2}$`)
)

// Static declaration guard: compare ALL setup action pins and inputs, not a
// runtime wiring claim. Checkout refs intentionally differ; its action must not.
func workflowSetupPins(body, job string) (map[string]string, error) {
	start := strings.Index(body, "\n  "+job+":\n")
	if start < 0 {
		return nil, errors.New("workflow job missing")
	}
	body = body[start+len("\n  "+job+":\n"):]
	if next := workflowJobStart.FindStringIndex(body); next != nil {
		body = body[:next[0]]
	}
	pins := make(map[string]string)
	current := ""
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "      - ") {
			current = ""
		}
		if strings.HasPrefix(line, "    runs-on: ") {
			pins["runner"] = strings.TrimSpace(strings.TrimPrefix(line, "    runs-on: "))
		}
		if strings.HasPrefix(line, "      - uses: ") {
			fields := strings.Fields(strings.TrimPrefix(line, "      - uses: "))
			if len(fields) == 0 {
				return nil, errors.New("empty setup action")
			}
			ref := fields[0]
			action, sha, ok := strings.Cut(ref, "@")
			if !ok || !shaPattern.MatchString(sha) {
				return nil, errors.New("setup action is not commit-pinned")
			}
			key := "action/" + action
			if old, exists := pins[key]; exists && old != sha {
				return nil, errors.New("one job pins different commits of the same action")
			}
			pins[key], current = sha, action
		}
		if current != "" && current != "actions/checkout" && strings.HasPrefix(line, "          ") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
			if !ok {
				return nil, errors.New("unrecognised setup input shape")
			}
			key, value = "input/"+current+"/"+key, strings.TrimSpace(value)
			if old, exists := pins[key]; exists && old != value {
				return nil, errors.New("one job sets different inputs on the same setup action")
			}
			pins[key] = value
		}
	}
	return pins, nil
}

func releaseRehearsalParity(release, rehearsal, mise string) error {
	prod, err := workflowSetupPins(release, "release")
	if err != nil {
		return err
	}
	scratch, err := workflowSetupPins(rehearsal, "full-publication")
	if err != nil {
		return err
	}
	if !maps.Equal(prod, scratch) {
		return errors.New("release and rehearsal setup action pins/inputs differ")
	}
	var cfg struct{ Tools map[string]string }
	if _, err = toml.Decode(mise, &cfg); err != nil {
		return err
	}
	for _, name := range []string{"cosign", "syft", "goreleaser"} {
		if !versionPattern.MatchString(cfg.Tools[name]) {
			return fmt.Errorf("shared mise tool %s must have an exact version", name)
		}
	}
	if prod["input/sigstore/cosign-installer/cosign-release"] != "v"+cfg.Tools["cosign"] ||
		prod["input/anchore/sbom-action/download-syft/syft-version"] != "v"+cfg.Tools["syft"] {
		return errors.New("cosign/syft setup must use the exact shared mise tool versions")
	}
	if !miseVersionPin.MatchString(prod["input/jdx/mise-action/version"]) ||
		prod["input/jdx/mise-action/experimental"] != "true" {
		return errors.New("shared mise setup version/options missing")
	}
	for key := range prod {
		if strings.HasPrefix(key, "input/jdx/mise-action/") && key != "input/jdx/mise-action/version" && key != "input/jdx/mise-action/experimental" {
			return errors.New("mise setup cannot override the shared tool file, including goreleaser")
		}
	}
	return nil
}

func TestReleaseAndRehearsalPinIdenticalToolchains(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		body, err := os.ReadFile("../../" + path)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	release, rehearsal, mise := read(workflowPath), read(publicationWorkflow), read("mise.toml")
	if err := releaseRehearsalParity(release, rehearsal, mise); err != nil {
		t.Fatal(err)
	}
	mutations := []struct{ from, to string }{
		{"cosign-release: v3.1.3", "cosign-release: v3.1.2"},
		{"syft-version: v1.50.0", "syft-version: v1.49.0"},
		{"version: 2026.7.7", "version: 2026.7.6"},
		{"experimental: true", "experimental: true\n          install_args: goreleaser@2.16.0"},
	}
	// Action pins are read from the rehearsal workflow rather than written
	// here: a copied SHA went stale on the first Dependabot bump and reported
	// the guard as broken when only the fixture was. The list of actions is
	// still fixed, so a pin vanishing from the workflow fails instead of
	// quietly shrinking what this test mutates.
	pins := map[string]string{}
	for _, m := range regexp.MustCompile(`uses: ([^@\s]+)@([0-9a-f]{40})`).FindAllStringSubmatch(rehearsal, -1) {
		pins[m[1]] = m[2]
	}
	for _, action := range []string{
		"jdx/mise-action", "sigstore/cosign-installer", "anchore/sbom-action/download-syft", "actions/checkout",
	} {
		sha, ok := pins[action]
		if !ok {
			t.Fatalf("rehearsal workflow no longer pins %s by SHA", action)
		}
		mutations = append(mutations, struct{ from, to string }{
			action + "@" + sha, action + "@" + strings.Repeat("1", 40),
		})
	}
	for _, mutation := range mutations {
		changed := strings.ReplaceAll(rehearsal, mutation.from, mutation.to)
		if changed == rehearsal || releaseRehearsalParity(release, changed, mise) == nil {
			t.Fatalf("guard missed setup drift: %s", mutation.from)
		}
	}
	if releaseRehearsalParity(release, rehearsal, strings.ReplaceAll(mise, `goreleaser = "2.17.1"`, `goreleaser = "latest"`)) == nil {
		t.Fatal("unversioned GoReleaser accepted")
	}
}
