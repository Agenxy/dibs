package main

import "testing"

// A review that did not end with the count did not happen, whatever it
// printed above. The brief is quoted back by some reviewers, so only the last
// FINDINGS line counts, and a malformed one counts for nothing.
func TestOnlyAClosingFindingsLineCountsAsAReview(t *testing.T) {
	if _, ok := findingsLine("I could not read the diff.\n"); ok {
		t.Error("a review with no FINDINGS line was accepted")
	}
	if n, ok := findingsLine("...\nFINDINGS: 0\n"); !ok || n != 0 {
		t.Errorf("a clean surface must count as reviewed: ok=%v n=%d", ok, n)
	}
	quoted := "the brief says to end with\n    FINDINGS: <count>\nthen:\n1. core/x.go:12 ...\nFINDINGS: 1\n"
	if n, ok := findingsLine(quoted); !ok || n != 1 {
		t.Errorf("the quoted brief line must not be mistaken for the answer: ok=%v n=%d", ok, n)
	}
	if _, ok := findingsLine("FINDINGS: <count>\n"); ok {
		t.Error("the brief's placeholder alone is not a count")
	}
}
