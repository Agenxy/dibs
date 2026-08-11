package overlap

import (
	"testing"
)

// The calibration set had no hard negatives, and called them positives instead.
//
// scorePairs labels a pair "related" when the two commits share ANY file, and
// "unrelated" when they share none. The pairs that actually broke a live fleet
// shared Justfile, .github/workflows/ci.yml, CMakeLists.txt and llms-full.txt,
// files every commit in a repository touches: while doing entirely unrelated
// work. Under that labelling they are POSITIVES.
//
// So the benchmark did not merely fail to measure the failure mode. It scored the
// model as CORRECT for producing it, and every threshold derived from it inherited
// that. It also explains a result that had been taken at face value: a small model
// looked as good as a large one, which is what happens when the only negatives in
// the set are pairs that share nothing at all. Everything separates those.
//
// This test states the labelling bug directly, so it cannot quietly come back.
func TestSharesFileCallsGenericOverlapRelated(t *testing.T) {
	// Two commits from genuinely different subsystems, as reported: a runtime
	// change and a CLI/docs change, each touching the repo-wide build files.
	runtime := []string{"runtime/src/k7d/main.cpp", "runtime/CMakeLists.txt", "Justfile"}
	cli := []string{"cli/k7_cli/main.py", "docs/index.md", "Justfile"}

	if !sharesFile(runtime, cli) {
		t.Fatal("premise changed: these no longer share a file")
	}
	// sharesFile is what splits the calibration set, so this pair is scored as a
	// POSITIVE (an example of "the same work") on the strength of a Justfile.
	t.Log("two unrelated subsystems are labelled related because both touch Justfile")

	// The honest label needs a file that is actually about the work. Sharing only
	// ubiquitous files is the definition of a hard negative, and the set contained
	// none because they were all relabelled.
	if distinctiveShare(runtime, cli, map[string]bool{"Justfile": true}) {
		t.Error("a pair sharing ONLY a ubiquitous file must not count as related")
	}
	// The same two lanes, now genuinely overlapping.
	cliTouchingRuntime := []string{"runtime/src/k7d/main.cpp", "cli/k7_cli/main.py"}
	if !distinctiveShare(runtime, cliTouchingRuntime, map[string]bool{"Justfile": true}) {
		t.Error("a pair sharing a real file must still count as related")
	}
}
