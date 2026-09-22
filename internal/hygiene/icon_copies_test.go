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
