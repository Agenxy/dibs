package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture builds a miniature repository: a changelog with something to release
// and one manifest, which is all Stamp touches.
func fixture(t *testing.T, changelog string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, Changelog), []byte(changelog), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, rel := range Manifests {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		body := `{
  "name": "x",
  "version": "0.0.5",
  "description": "y"
}
`
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const withNotes = `# Changelog

## [Unreleased]

### Added

- Something worth shipping.

## [0.0.5] - 2026-08-14

- The one before.
`

// The stamp writes the version everywhere at once, which is the whole point:
// the manifests drifted because a human wrote them one at a time and stopped.
func TestStampWritesEveryManifestAndKeepsAnUnreleasedHeading(t *testing.T) {
	root := fixture(t, withNotes)
	changed, err := Stamp(root, "0.0.6")
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != len(Manifests)+1 {
		t.Errorf("changed %d files, want the changelog plus %d manifests: %v",
			len(changed), len(Manifests), changed)
	}
	for _, rel := range Manifests {
		blob, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Name, Version, Description string
		}
		if err := json.Unmarshal(blob, &doc); err != nil {
			t.Fatalf("%s is no longer JSON after stamping: %v", rel, err)
		}
		if doc.Version != "0.0.6" {
			t.Errorf("%s version = %q, want 0.0.6", rel, doc.Version)
		}
		// The edit is textual precisely so a human-maintained file keeps its
		// shape: a JSON round-trip would reorder keys and reformat a file
		// somebody reads, turning a one-line stamp into an unreviewable diff.
		if doc.Name != "x" || doc.Description != "y" {
			t.Errorf("%s lost its other fields: %+v", rel, doc)
		}
		if !strings.Contains(string(blob), "\n  \"version\"") {
			t.Errorf("%s was reformatted rather than edited:\n%s", rel, blob)
		}
	}

	body, err := os.ReadFile(filepath.Join(root, Changelog))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "## [0.0.6] - ") {
		t.Errorf("the released heading is missing:\n%s", text)
	}
	// Without a fresh Unreleased heading the next change is appended to a
	// version that has already shipped.
	if !strings.Contains(text, Unreleased) {
		t.Error("no Unreleased heading survived, so the next change has nowhere to go")
	}
	if strings.Index(text, Unreleased) > strings.Index(text, "## [0.0.6]") {
		t.Error("the Unreleased heading is below the release it precedes")
	}
	if got, err := Current(root); err != nil || got != "0.0.6" {
		t.Errorf("Current = %q, %v; want 0.0.6", got, err)
	}
}

// A release that goes backwards leaves every installer offering an older build
// than the one before it, and the tag cannot be moved once it has published.
func TestStampRefusesAVersionThatIsNotNewer(t *testing.T) {
	for _, v := range []string{"0.0.5", "0.0.4", "0.0"} {
		root := fixture(t, withNotes)
		if _, err := Stamp(root, v); err == nil {
			t.Errorf("Stamp(%q) was allowed over an existing 0.0.5", v)
		}
	}
}

// A version with no notes is worse than no release: the tag exists, the
// artifacts publish, and the one document that says what changed says nothing.
func TestStampRefusesAnEmptyUnreleasedSection(t *testing.T) {
	root := fixture(t, "# Changelog\n\n## [Unreleased]\n\n## [0.0.5] - 2026-08-14\n\n- The one before.\n")
	_, err := Stamp(root, "0.0.6")
	if err == nil {
		t.Fatal("a release with no notes was allowed")
	}
	if !strings.Contains(err.Error(), "nothing to release") {
		t.Errorf("err = %v: it does not say what is wrong", err)
	}
	// And nothing was written: a refusal that half-stamped would be worse than
	// one that never ran.
	if got, _ := Current(root); got != "0.0.5" {
		t.Errorf("the changelog was modified by a refused stamp: now %q", got)
	}
}
