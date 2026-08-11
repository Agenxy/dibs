package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A syntax error in the embedded panel script fails silently: the browser runs
// nothing, the static shell still renders, and the human sees a panel with no
// content: indistinguishable from the host-side rendering bugs we spent hours
// chasing. That shipped once, when splicing out a block took a closing brace
// with it.
//
// The check shells out to bun (this project's JS toolchain) rather than
// hand-rolling a parser.
// An earlier attempt counted braces and was defeated immediately by a regex
// literal (`/[&<>"']/g`) whose brackets and quotes are not delimiters at all,
// writing a JS tokenizer in order to test JS is how you get two bugs.
func TestPanelScriptParses(t *testing.T) {
	requireFullPanel(t)
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not available; cannot parse-check the panel script")
	}
	// Parse the ASSEMBLED panel, concatenated in document order: the shared
	// component library and the panel's own script are separate <script> blocks
	// and share a scope at runtime, so checking either alone would miss a break
	// in the other. Taking the span from the first <script> to the last would
	// splice the intervening "</script><script>" into the source and fail on
	// that instead, which is how this test first broke when the library was
	// extracted.
	html := boardApp()
	var js strings.Builder
	for rest := html; ; {
		start := strings.Index(rest, "<script>")
		if start < 0 {
			break
		}
		rest = rest[start+len("<script>"):]
		end := strings.Index(rest, "</script>")
		if end < 0 {
			t.Fatal("unterminated <script> block in the panel template")
		}
		js.WriteString(rest[:end])
		js.WriteString("\n")
		rest = rest[end:]
	}
	if js.Len() == 0 {
		t.Fatal("no script block in the panel template")
	}

	f := filepath.Join(t.TempDir(), "panel.js")
	if err := os.WriteFile(f, []byte(js.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bun, "build", f, "--target=browser",
		"--outfile="+filepath.Join(t.TempDir(), "out.js")).CombinedOutput()
	if err != nil {
		t.Fatalf("panel script does not parse: it would run as nothing:\n%s", out)
	}
}
