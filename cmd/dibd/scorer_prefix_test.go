package main

import (
	"testing"

	"github.com/agenxy/dibs/internal/overlap"
)

// An explicitly EMPTY marker must reach the embedder.
//
// Precedence resolved `-match-embed-query-prefix ""` correctly and the embedder
// construction then dropped it, because the guard tested the VALUES rather than
// whether they were given, so the model's inferred marker came back on the
// wire. SetAffixes documents both-empty as "disable markers, do not detect
// again", which is a real configuration: it is what you pass for a model whose
// card states it needs none, and therefore exactly when an operator reaches
// for it.
func TestAnExplicitlyEmptyMarkerCountsAsGiven(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*scorerFlags)
		want  bool
	}{
		{"nothing given", func(*scorerFlags) {}, false},
		{"a query marker given", func(f *scorerFlags) { f.embedQueryPrefix = "query: " }, true},
		{
			"an EMPTY query marker given explicitly",
			func(f *scorerFlags) { f.set["embed-query-prefix"] = true }, true,
		},
		{
			"an EMPTY doc marker given explicitly",
			func(f *scorerFlags) { f.set["embed-doc-prefix"] = true }, true,
		},
	} {
		f := defaultScorerFlags()
		c.setup(f)
		if got := f.markersGiven(); got != c.want {
			t.Errorf("%s: markersGiven()=%v, want %v", c.name, got, c.want)
		}
	}
}

// And the effect that decision has: empty markers must DISABLE detection, not
// fall back to it, or a model configured to take none gets one anyway.
func TestEmptyMarkersDisableDetectionRatherThanRestoringIt(t *testing.T) {
	// A model Dibs recognises, so it would otherwise be marked.
	em := overlap.NewEmbed("http://example.invalid", "qwen3-embedding:0.6b", "", 0)
	if q, _ := em.Affixes(); q == "" {
		t.Fatal("setup: this model should carry an inferred marker to begin with")
	}
	em.SetAffixes("", "")
	if q, d := em.Affixes(); q != "" || d != "" {
		t.Errorf("empty markers must disable them, got query=%q doc=%q", q, d)
	}
}
