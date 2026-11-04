// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package assets

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestStreamedStylesPreserveMaterializedBytes(t *testing.T) {
	// The pre-streaming formula is an independent byte oracle, not a second
	// production encoding path. Fonts, licences and ordering all affect hashes.
	var faces strings.Builder
	for _, face := range []struct {
		family, weight string
		data           []byte
	}{
		{"Atkinson Hyperlegible Next", "200 800", AtkinsonNext},
		{"Atkinson Hyperlegible Mono", "200 800", AtkinsonMono},
	} {
		fmt.Fprintf(&faces,
			"@font-face{font-family:'%s';font-style:normal;font-weight:%s;"+
				"font-display:swap;src:url(data:font/ttf;base64,%s) format('truetype')}",
			face.family, face.weight, base64.StdEncoding.EncodeToString(face.data))
	}
	want := "/*\n" + nextLicense + "\n" + monoLicense + "\n*/\n" + faces.String() + "\n" + boardCSS + "\n" + signalCSS
	var streamed strings.Builder
	if err := WriteStyles(&streamed); err != nil {
		t.Fatal(err)
	}
	if streamed.String() != want || Styles() != want || FontFaces() != faces.String() {
		t.Fatal("shared style writer changed the materialized stylesheet bytes")
	}
}
