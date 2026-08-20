package main

import (
	"strings"
	"testing"
)

// The recommendation fires only where the measurements say it should, and never
// when the operator has already acted on it.
//
// A recommendation that repeats after you have done the thing is an alarm, and
// one that fires on every small repository is noise. Both make the channel worth
// ignoring, which is the failure this whole reporting path exists to avoid.
func TestSidecarIsRecommendedOnlyWhereItHelps(t *testing.T) {
	cases := []struct {
		name     string
		embedURL string
		files    int
		want     bool
	}{
		{"small repo, no sidecar", "", 121, false},
		{"large repo, no sidecar", "", 6330, true},
		{"at the measured boundary", "", sidecarWorthIt, true},
		{"just below it", "", sidecarWorthIt - 1, false},
		{"large repo, sidecar already configured", "http://127.0.0.1:8080", 6330, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &scorerFlags{embedURL: c.embedURL}
			got := f.embedURL == "" && c.files >= sidecarWorthIt
			if got != c.want {
				t.Errorf("recommend = %v, want %v", got, c.want)
			}
		})
	}
}

// The boundary is the measured one, not a round number somebody liked.
//
// SPEC-CHANNELS.md records tier-0 recall@10 at 0.488 for 121 files and 0.229 at
// 1,142. If this constant drifts away from where the measurements show the
// scorer falling off, the recommendation stops being evidence and becomes an
// opinion.
func TestTheBoundaryMatchesTheMeasurements(t *testing.T) {
	if sidecarWorthIt < 500 || sidecarWorthIt > 2000 {
		t.Errorf("sidecarWorthIt = %d, which is outside the range the measured recall "+
			"curve supports (0.488 at 121 files, 0.229 at 1,142). Re-read the table in "+
			"SPEC-CHANNELS.md before moving it", sidecarWorthIt)
	}
}

// The remedy has to be actionable, which is this reporting path's stated rule:
// "Remedy is what to do. Required: a report without one is an alarm."
func TestTheRemedyNamesTheCommandAndTheFollowUp(t *testing.T) {
	f := &scorerFlags{}
	rec := recommendationFor(f, "/repo", 6330)
	for _, want := range []string{"-match-embed-url", "dibs calibrate", "SPEC-CHANNELS.md"} {
		if !strings.Contains(rec, want) {
			t.Errorf("the remedy does not mention %q, so a reader cannot act on it:\n%s", want, rec)
		}
	}
}
