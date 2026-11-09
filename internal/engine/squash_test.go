// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"strings"
	"testing"
)

// squashSpace stops early, so it is only correct where the caller truncates:
// every truncation used at its call sites has to come out exactly as it did
// from the full normalisation.
func TestSquashSpaceTruncatesLikeTheFullNormalisation(t *testing.T) {
	inputs := []string{
		"", "   ", "one", "  lead and trail  ", "tabs\tand\nnewlines\r\n here",
		strings.Repeat("word ", 300), strings.Repeat("é ", 150) + "tail",
		strings.Repeat("x", 500), "a" + strings.Repeat(" ", 400) + "b",
		strings.Repeat("日本語 ", 90), strings.Repeat("-", 199) + " - end",
	}
	for _, in := range inputs {
		full := strings.Join(strings.Fields(in), " ")
		for _, n := range []int{0, 1, 5, 80, 199, 200, 201, 1000} {
			got := squashSpace(in, n)
			if !strings.HasPrefix(full, got) {
				t.Fatalf("squashSpace(%q, %d) = %q, not a prefix of %q", in, n, got, full)
			}
			if a, b := trimRunes(got, n), trimRunes(full, n); a != b {
				t.Errorf("trimRunes differs for %q at %d: %q vs %q", in, n, a, b)
			}
			byteCut := func(s string) string {
				if len(s) > n {
					return s[:n] + "..."
				}
				return s
			}
			if a, b := byteCut(got), byteCut(full); a != b {
				t.Errorf("byte truncation differs for %q at %d: %q vs %q", in, n, a, b)
			}
		}
	}
}
