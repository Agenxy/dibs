// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package assets

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Both surfaces name their families in CSS; if FontFaces stops emitting one,
// the text silently falls back to a system face and nobody notices until it
// looks wrong. Removing the vendored serif made this worth pinning.
func TestFontFacesEmitsExactlyTheFamiliesTheTemplatesName(t *testing.T) {
	css := FontFaces()
	for _, want := range []string{"font-family:'Atkinson Hyperlegible Next'", "font-family:'Atkinson Hyperlegible Mono'"} {
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
	if !strings.Contains(css, "data:font/ttf;base64,") {
		t.Error("payloads are not inlined")
	}
}

// Native and web surfaces use the same complete upstream files, not the site's
// subset. The provenance's pinned hashes are guarded on the native side.
func TestBoardFontsAndLicensesMatchTheNativeIdentity(t *testing.T) {
	for _, name := range []string{"AtkinsonHyperlegibleNext[wght].ttf", "AtkinsonHyperlegibleMono[wght].ttf", "OFL-Next.txt", "OFL-Mono.txt", "PROVENANCE.md"} {
		board, err := os.ReadFile("fonts/" + name)
		if err != nil {
			t.Fatal(err)
		}
		native, err := os.ReadFile("../notify/app/fonts/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(board, native) {
			t.Errorf("%s differs from the canonical full native file", name)
		}
	}
	for _, notice := range []string{nextLicense, monoLicense} {
		if !strings.Contains(Styles(), notice) {
			t.Error("inlined font lost its licence")
		}
	}
}
