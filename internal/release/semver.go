package release

import (
	"fmt"
	"strconv"
	"strings"
)

// Version ordering, in one place, because two things now need it and they
// need different amounts of it.
//
// `Stamp` compares two release numbers and nothing else: it refuses a version
// that is not newer than the one the changelog records, and a release target
// is always a plain MAJOR.MINOR.PATCH. The update check compares what a binary
// SAYS IT IS against what is published, and what a binary says it is can be a
// pseudo-version like `0.0.8-0.20260922110423-ed07e4883c86`, which semver
// orders BELOW `0.0.8`: correct, and exactly what an operator running a source
// build between two releases should be told.
//
// So the ordering understands prereleases and the stamper keeps refusing to
// stamp one. Those are different questions and it was tempting to answer them
// with two functions; then there would be two answers to "which of these is
// newer", and this repository has spent five releases learning what that costs.

// Comparable reports whether v can be ordered against a release number at all.
//
// A binary built from a checkout with no tag reachable reports a bare revision
// or the word "devel", and `internal/build` chose those deliberately over a
// number that could be compared and would be wrong. Honouring that choice here
// means saying "cannot be compared" rather than inventing an ordering: an
// operator told their build is out of date, when nothing knows what their
// build is, would be told it forever.
func Comparable(v string) bool {
	_, _, err := semver(v)
	return err == nil
}

// Newer reports whether a is a later version than b.
func Newer(a, b string) (bool, error) {
	c, err := Compare(a, b)
	return c > 0, err
}

// Compare returns -1, 0 or 1 as a sorts before, equal to, or after b, by the
// rules in semver 2.0.0 §11. Build metadata is ignored, as §10 requires.
func Compare(a, b string) (int, error) {
	an, apre, err := semver(a)
	if err != nil {
		return 0, err
	}
	bn, bpre, err := semver(b)
	if err != nil {
		return 0, err
	}
	for i := range an {
		if an[i] != bn[i] {
			return sign(an[i] - bn[i]), nil
		}
	}
	return comparePrerelease(apre, bpre), nil
}

// semver splits a version into its three numbers and its prerelease, dropping
// any build metadata.
func semver(v string) ([3]int, string, error) {
	var nums [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "+") // build metadata takes no part in ordering
	core, pre, _ := strings.Cut(v, "-")
	f := strings.Split(core, ".")
	if len(f) != 3 {
		return nums, "", fmt.Errorf("%q is not a version: want MAJOR.MINOR.PATCH", v)
	}
	for i, s := range f {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || (len(s) > 1 && s[0] == '0') {
			return nums, "", fmt.Errorf("%q is not a version: field %d is not a number", v, i+1)
		}
		nums[i] = n
	}
	return nums, pre, nil
}

// comparePrerelease implements §11.3 and §11.4: a version WITH a prerelease is
// lower than the same version without one, and otherwise the dot separated
// identifiers are compared left to right, numerically when both are numeric,
// as text when they are not, with numeric ranking below alphanumeric.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1 // 1.0.0 is AFTER 1.0.0-rc.1
	case b == "":
		return -1
	}
	af, bf := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(af) && i < len(bf); i++ {
		if c := compareIdentifier(af[i], bf[i]); c != 0 {
			return c
		}
	}
	// Every identifier they share is equal, so the one with more of them wins.
	return sign(len(af) - len(bf))
}

func compareIdentifier(a, b string) int {
	an, aNum := strconv.Atoi(a)
	bn, bNum := strconv.Atoi(b)
	switch {
	case aNum == nil && bNum == nil:
		return sign(an - bn)
	case aNum == nil:
		return -1 // numeric identifiers rank below alphanumeric ones
	case bNum == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
