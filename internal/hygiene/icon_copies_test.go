package hygiene

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// One mark, wherever it is shipped.
//
// The icon exists three times because two of the places that need it
// cannot reach out of their own directory: `go:embed` in tools/mcpbundle
// and an MCPB manifest's `icon` field, which names a file beside itself.
// Same constraint as SKILLS.md and internal/mcp/skills.md, so the same
// answer: one canonical file, copies beside the things that need them,
// and a test that fails when they drift.
//
// It is worth a test because the failure is silent and cosmetic-looking
// and is neither. An extension that ships a stale mark, or a mark that
// does not match the app icon beside it, is the kind of thing that reads
// as "assembled from parts" to everyone who installs it, and nothing in a
// build would ever complain.
func TestTheMarkIsTheSameFileEverywhereItShips(t *testing.T) {
	root := repoRoot(t)
	const canonical = "docs/icon-512.png"
	want, err := os.ReadFile(filepath.Join(root, canonical)) // #nosec G304 -- a path in this repository
	if err != nil {
		t.Fatalf("the canonical mark is missing (%s): every copy below is of it", err)
	}
	if len(want) < 1024 {
		t.Fatalf("%s is %d bytes: too small to be the rendered mark, and a test that "+
			"compares three copies of nothing passes", canonical, len(want))
	}

	// The SVG pair, which is the same problem in the other format and has
	// already bitten: `docs/icon.svg` and `internal/assets/icon.svg` are
	// copies because go:embed cannot reach above its own package, a fix
	// once landed on the docs one alone, and the copy COMPILED INTO THE
	// BINARY stayed broken while the repository looked repaired. The
	// well-formedness check beside this one records that and does not
	// compare them, so a mark changed in one file and not the other would
	// pass both. Round after the mark changed, 2026-09-22.
	svgWant, err := os.ReadFile(filepath.Join(root, "internal/assets/icon.svg")) // #nosec G304
	if err != nil {
		t.Fatalf("the embedded mark is missing (%s): it is what the board serves", err)
	}
	svgGot, err := os.ReadFile(filepath.Join(root, "docs/icon.svg")) // #nosec G304
	if err != nil {
		t.Errorf("docs/icon.svg is missing: README.md renders it")
	} else if !bytes.Equal(svgGot, svgWant) {
		t.Errorf("docs/icon.svg has drifted from internal/assets/icon.svg (%s vs %s). "+
			"The second is the one go:embed compiles into the binary and the board "+
			"serves; the first is the one a reader sees in the README. A fix that "+
			"lands on the docs copy alone leaves the shipped mark broken and the "+
			"repository looking repaired, which has happened.",
			short(svgGot), short(svgWant))
	}

	// Each copy, and what ships it.
	for path, why := range map[string]string{
		"tools/mcpbundle/icon-512.png": "the MCP Bundle embeds this one (go:embed cannot " +
			"reach above its package) and the manifest names it as `icon`",
		"plugins/claude-desktop/icon.png": "the Claude Desktop manifest names this one, " +
			"and an MCPB `icon` is a path beside the manifest",
		"internal/assets/icon-512.png": "the board serves this one as its apple-touch-icon, " +
			"which iOS has no SVG form of",
	} {
		got, err := os.ReadFile(filepath.Join(root, path)) // #nosec G304 -- a path in this repository
		if err != nil {
			t.Errorf("%s is missing: %s", path, why)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s has drifted from %s (%s vs %s). %s. Copy the canonical one over it: "+
				"two marks is the same defect as two vocabularies, and this one is visible "+
				"to everybody who installs Dibs.",
				path, canonical, short(got), short(want), why)
		}
	}
}

func short(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}
