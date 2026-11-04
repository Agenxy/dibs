// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package assets holds the visual material shared by every Dibs surface: the
// MCP Apps board panel and the web board, so the two render as one product
// rather than drifting into two house styles.
//
// The surfaces are deliberately NOT the same page. The panel is one agent's own
// board and mailbox, authenticated by that agent's token and rendered inside a
// host's sandboxed iframe; the web board is the operator's god view over every
// agent and all mail, behind the admin password. They answer different questions
// for different readers. What they share is what an agent looks like, what a
// message looks like, and what an event looks like, so this package holds the
// design system and the components, and each surface composes its own page.
//
// Everything here is INLINED into the HTML rather than served as a file. That
// is a hard requirement, not a preference: the panel declares a CSP with no
// external origins (see internal/mcp/apps.go), so any stylesheet, script or
// font fetched over the network would fail closed and silently.
//
// Atkinson Hyperlegible Next and Mono are unmodified upstream SIL OFL faces.
// fonts/PROVENANCE.md records their source and hashes; the licenses ship beside
// them. The same full font bytes are used by the native prompt.
package assets

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"
)

// AtkinsonNext is the vendored sans face, inlined so no surface reaches a CDN.
//
//go:embed fonts/AtkinsonHyperlegibleNext*.ttf
var AtkinsonNext []byte

// AtkinsonMono is the vendored mono face, inlined so no surface reaches a CDN.
//
//go:embed fonts/AtkinsonHyperlegibleMono*.ttf
var AtkinsonMono []byte

// Licenses accompany the inlined bytes in every shipped HTML artifact.
//
//go:embed fonts/OFL-Next.txt
var nextLicense string

//go:embed fonts/OFL-Mono.txt
var monoLicense string

// Icon is the Dibs mark, served as the favicon by both surfaces.
//
// A tab with no favicon shows the browser's blank-page glyph, which is what an
// unfinished tool looks like, and the board is a page people leave open all
// day beside their editor. Embedded like everything else here: the panel's CSP
// admits no external origin, so a linked icon would fail closed and silently.
//
//go:embed icon.svg
var Icon string

// IconPNG is the same mark as a raster, for the surfaces that cannot take
// an SVG. iOS is the one that matters: `apple-touch-icon` has no SVG form,
// so a board added to a home screen got a screenshot of itself, which is
// unreadable at that size and is the default nobody chose. Same file as
// the macOS app icon and the MCP bundle's, held to that by
// TestTheMarkIsTheSameFileEverywhereItShips.
//
//go:embed icon-512.png
var IconPNG []byte

//go:embed board.css
var boardCSS string

//go:embed signal.css
var signalCSS string

//go:embed board.js
var boardJS string

var cachedFaces = sync.OnceValue(func() string {
	var b strings.Builder
	_ = writeFontFaces(&b) // strings.Builder cannot fail.
	return b.String()
})

var cachedStyles = sync.OnceValue(func() string {
	var b strings.Builder
	_ = WriteStyles(&b) // strings.Builder cannot fail.
	return b.String()
})

func writeFontFaces(w io.Writer) error {
	for _, face := range []struct {
		family, weight string
		data           []byte
	}{
		{"Atkinson Hyperlegible Next", "200 800", AtkinsonNext},
		{"Atkinson Hyperlegible Mono", "200 800", AtkinsonMono},
	} {
		if _, err := fmt.Fprintf(w, "@font-face{font-family:'%s';font-style:normal;font-weight:%s;"+
			"font-display:swap;src:url(data:font/ttf;base64,", face.family, face.weight); err != nil {
			return err
		}
		encoder := base64.NewEncoder(base64.StdEncoding, w)
		if _, err := encoder.Write(face.data); err != nil {
			return err
		}
		if err := encoder.Close(); err != nil {
			return err
		}
		if _, err := io.WriteString(w, ") format('truetype')}"); err != nil {
			return err
		}
	}
	return nil
}

// WriteStyles writes the same stylesheet as Styles without materializing or
// caching its base64 font HTML. Content hashes use this path; rendering can
// retain the materialized string. Both have exactly one encoding rule.
func WriteStyles(w io.Writer) error {
	for _, s := range []string{"/*\n", nextLicense, "\n", monoLicense, "\n*/\n"} {
		if _, err := io.WriteString(w, s); err != nil {
			return err
		}
	}
	if err := writeFontFaces(w); err != nil {
		return err
	}
	for _, s := range []string{"\n", boardCSS, "\n", signalCSS} {
		if _, err := io.WriteString(w, s); err != nil {
			return err
		}
	}
	return nil
}

// FontFaces returns deterministic @font-face rules with inline TrueType data.
func FontFaces() string { return cachedFaces() }

// Styles is the complete stylesheet shared by both surfaces: licensed inline
// faces, then the board and signal styles. It materializes only when requested.
func Styles() string { return cachedStyles() }

// BoardJS is the shared component library: pure functions from board data to
// HTML strings, exposed as a `Board` object. It contains no transport and no
// state, because the two surfaces get their data by completely different means
// postMessage from an MCP host, and server-sent events from dibd.
func BoardJS() string { return boardJS }
