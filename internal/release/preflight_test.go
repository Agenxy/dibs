package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A manifest this tool can see is wrong must stop it before anything is written.
//
// THE HALF-RELEASE THIS PREVENTS. Stamp wrote the changelog first and then
// rewrote manifests one at a time, returning on the first failure. A manifest
// that is unwritable or does not carry its version in the expected form
// therefore left the changelog stamped and some of the manifests updated, and
// `Current` reads the version out of the changelog, so the "must be newer"
// check refuses the retry. The one step before the tag ends up half-done and
// unrepeatable, and repairing it by hand is the thing this command exists to
// stop anybody doing.
//
// Found by a pre-release review, hours before I ran it for real.
func TestABadManifestStopsStampBeforeAnythingIsWritten(t *testing.T) {
	root := t.TempDir()

	changelog := filepath.Join(root, Changelog)
	before := Unreleased + "\n\n- something worth releasing\n\n## [0.0.5] - 2026-08-14\n"
	if err := os.WriteFile(changelog, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	// Every manifest present and stampable except the last, which carries no
	// version field at all.
	for i, rel := range Manifests {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"name": "x", "version": "0.0.5"}`
		if i == len(Manifests)-1 {
			body = `{"name": "x"}`
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Stamp(root, "0.0.6"); err == nil {
		t.Fatal("a manifest with no version field was accepted")
	}
	assertUntouched(t, changelog, before, root)
}

// And the same for a manifest this process cannot WRITE.
//
// The first version of the pre-flight proved only that each manifest parses and
// could be rewritten in memory, which says nothing about permissions, a
// read-only mount, or a file somebody made immutable. A real write failure
// therefore still landed mid-sequence with the changelog already stamped, and
// Current then refuses the retry. Found by the review that had already found
// the un-transactional write once.
func TestAnUnwritableManifestStopsStampToo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only file is still writable")
	}
	root := t.TempDir()
	changelog := filepath.Join(root, Changelog)
	before := Unreleased + "\n\n- something worth releasing\n\n## [0.0.5] - 2026-08-14\n"
	if err := os.WriteFile(changelog, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, rel := range Manifests {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(`{"name": "x", "version": "0.0.5"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if i == len(Manifests)-1 {
			if err := os.Chmod(full, 0o400); err != nil { // readable, not writable
				t.Fatal(err)
			}
		}
	}

	if _, err := Stamp(root, "0.0.6"); err == nil {
		t.Fatal("a manifest this process cannot write was accepted")
	}
	assertUntouched(t, changelog, before, root)
}

func assertUntouched(t *testing.T, changelog, before, root string) {
	t.Helper()

	// THE POINT: the changelog is untouched, so the command can simply be run
	// again once the manifest is fixed.
	after, err := os.ReadFile(changelog)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Errorf("the changelog was stamped before the manifests were checked, so "+
			"Current now reports the new version and a second run is refused as "+
			"not newer. The release is half-done and needs repairing by hand:\n%s",
			string(after))
	}
	if !strings.Contains(string(after), Unreleased) {
		t.Error("the Unreleased heading is gone")
	}

	// And the manifests that WOULD have been stamped are untouched too.
	first, err := os.ReadFile(filepath.Join(root, Manifests[0]))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), `"0.0.5"`) {
		t.Errorf("the first manifest was stamped before the last was checked: %s", first)
	}
}
