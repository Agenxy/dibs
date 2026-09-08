package hygiene

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every SVG this repository ships must actually parse.
//
// An SVG that is not well-formed XML does not degrade, it does not render: the
// browser stops at the first error and draws nothing. So the failure is total
// and completely silent to everything except an eye on the page, which is how
// the project icon shipped broken from the beginning and was noticed by a
// person rather than by the gate.
//
// The specific trap is worth naming, because it looks like prose and is not.
// A `--` sequence is illegal INSIDE an XML comment, and these files carry long
// design-rationale comments that naturally want to mention CSS custom
// properties by name. Writing `--accent` in that comment invalidates the whole
// document. Nothing about the file looks wrong, the colour is described
// correctly, and the icon is simply absent.
//
// It also drifted, which is the second half of the lesson. `docs/icon.svg` and
// `internal/assets/icon.svg` are copies, because go:embed cannot reach above
// its own package: the same reason SKILLS.md has a copy at
// internal/mcp/skills.md. The fix landed on the docs copy alone, so the one
// that is COMPILED INTO THE BINARY and served by the board stayed broken while
// the repository looked repaired. A guard aimed at one file would have gone
// green on exactly that.
//
// So this checks every tracked .svg, by parsing rather than by pattern. A rule
// that looked for `--` would miss the next malformation and flag legitimate
// ones inside path data.
func TestEveryShippedSVGIsWellFormedXML(t *testing.T) {
	root := repoRoot(t)
	checked := 0
	walk(t, root, func(rel, abs string) {
		if !strings.EqualFold(filepath.Ext(rel), ".svg") {
			return
		}
		body, err := os.ReadFile(abs) // #nosec G304 -- walking this repository
		if err != nil {
			t.Errorf("reading %s: %v", rel, err)
			return
		}
		checked++
		dec := xml.NewDecoder(strings.NewReader(string(body)))
		for {
			_, err := dec.Token()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Errorf("%s is not well-formed XML: %v\n"+
					"  An SVG that does not parse does not render AT ALL, and nothing "+
					"reports it. Note that `--` is illegal inside an XML comment, which "+
					"is what breaks these files when a rationale comment names a CSS "+
					"custom property like --accent.", rel, err)
				break
			}
		}
	})
	if checked == 0 {
		t.Fatal("no .svg files were parsed, so this check verified nothing")
	}
}
