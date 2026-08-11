package overlap

import "testing"

// A collapsed negative distribution must not delete the advisory band.
//
// join is the 95th percentile of unrelated pairs and notify their median. When
// those pairs score too alike the two percentiles land on the same value, and
// notify == join means every match either auto-joins or is invisible, with
// nothing in between and no warning.
//
// This is not hypothetical and it is not a test artifact: it began happening on
// Dibs' own repository once its history passed a few hundred commits, which is
// exactly the point at which an operator has enough data to trust the number.
// Driven from a synthetic distribution rather than from git history, because a
// test that reproduces this only when the repo happens to be large enough is a
// test that stops running.
func TestNotifyStaysBelowJoinWhenNegativesCollapse(t *testing.T) {
	// Every unrelated pair scoring identically is the degenerate case: median
	// and 95th percentile are the same number.
	identical := make([]float64, 40)
	for i := range identical {
		identical[i] = 0.2
	}
	join, notify, degenerate := thresholdsFrom(identical)
	if !degenerate {
		t.Errorf("a distribution where every negative scores alike was not reported "+
			"as degenerate (join=%v notify=%v)", join, notify)
	}
	if notify >= join {
		t.Errorf("notify=%v is not below join=%v; the suggest-only band has been "+
			"deleted, so every match either auto-joins or is invisible", notify, join)
	}
	if notify <= 0 {
		t.Errorf("notify=%v must stay positive, or nothing is ever suggested", notify)
	}

	// A healthy spread must NOT be flagged, or the warning means nothing.
	spread := make([]float64, 40)
	for i := range spread {
		spread[i] = float64(i) / 40
	}
	if _, _, deg := thresholdsFrom(spread); deg {
		t.Error("a well-spread distribution was reported as degenerate")
	}
}
