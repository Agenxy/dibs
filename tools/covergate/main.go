// covergate fails the build when total statement coverage falls below a floor.
//
// SPEC §17 asks for ≥85% on core+ledger and nothing enforced it: `task cover`
// printed the number and exited 0 whatever it said, so the gate was a sentence
// in a document. It sat at 85.6% (six tenths of a point of headroom) which is
// exactly the margin a single untested branch erases without anyone noticing.
//
// A Go program rather than a shell pipeline because this is a gate: it has to
// fail loudly and for the right reason. `go tool cover -func | tail -1 | awk`
// exits 0 when the profile is missing, when the format changes, and when awk
// parses nothing: all of which read as passing.
package main

import (
	"flag"
	"fmt"
	"os"

	"golang.org/x/tools/cover"
)

func main() {
	profile := flag.String("profile", "coverage.out", "coverage profile to read")
	min := flag.Float64("min", 85, "minimum total statement coverage, percent")
	flag.Parse()

	pct, statements, err := total(*profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "covergate: %v\n", err)
		os.Exit(2)
	}
	// A profile that covers nothing is the failure mode this check exists to
	// catch, and it would otherwise divide by zero and report a confident 0%.
	if statements == 0 {
		fmt.Fprintf(os.Stderr, "covergate: %s records no statements: nothing was measured\n", *profile)
		os.Exit(2)
	}
	if pct < *min {
		fmt.Fprintf(os.Stderr,
			"covergate: %.1f%% of %d statements, below the %.1f%% floor (SPEC §17).\n"+
				"  Run `go tool cover -html=%s` to see what is uncovered.\n",
			pct, statements, *min, *profile)
		os.Exit(1)
	}
	// Ignored deliberately: a gate that failed because it could not print its
	// success line would be worse than one that printed nothing.
	_, _ = fmt.Fprintf(os.Stdout, "covergate: %.1f%% of %d statements, floor %.1f%%: ok\n",
		pct, statements, *min)
}

// total is the same arithmetic `go tool cover -func` prints on its last line:
// covered statements over all statements, counting each block by its statement
// count rather than treating every block as one.
func total(path string) (pct float64, statements int, err error) {
	profiles, err := cover.ParseProfiles(path)
	if err != nil {
		return 0, 0, err
	}
	var covered, all int
	for _, p := range profiles {
		for _, b := range p.Blocks {
			all += b.NumStmt
			if b.Count > 0 {
				covered += b.NumStmt
			}
		}
	}
	if all == 0 {
		return 0, 0, nil
	}
	return 100 * float64(covered) / float64(all), all, nil
}
