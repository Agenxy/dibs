package assets

import (
	"strings"
	"testing"
)

// Both surfaces name their families in CSS; if FontFaces stops emitting one,
// the text silently falls back to a system face and nobody notices until it
// looks wrong. Removing the vendored serif made this worth pinning.
func TestFontFacesEmitsExactlyTheFamiliesTheTemplatesName(t *testing.T) {
	css := FontFaces()
	for _, want := range []string{"font-family:'Geist'", "font-family:'Geist Mono'"} {
		if !strings.Contains(css, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(css, "Newsreader") {
		t.Error("the removed serif is still being emitted")
	}
	if strings.Contains(css, "https://") {
		t.Error("a font is being fetched; the panel CSP forbids external origins")
	}
	if n := strings.Count(css, "@font-face"); n != 2 {
		t.Errorf("@font-face count = %d, want 2", n)
	}
	if !strings.Contains(css, "data:font/woff2;base64,") {
		t.Error("payloads are not inlined")
	}
}
