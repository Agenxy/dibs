package release

import "testing"

// The case the update check exists for, and the reason this file replaced a
// three-integer comparison: a source build reports a Go pseudo-version, and
// semver orders `0.0.8-0.<timestamp>-<rev>` BELOW `0.0.8`. That is the right
// answer and it is not the obvious one: the numbers are identical, and a
// comparator that reads only the numbers calls a build from between two
// releases up to date forever.
func TestASourceBuildIsOlderThanTheReleaseItsNumberNames(t *testing.T) {
	newer, err := Newer("0.0.8", "0.0.8-0.20260922110423-ed07e4883c86")
	if err != nil {
		t.Fatal(err)
	}
	if !newer {
		t.Fatal("the published 0.0.8 is newer than a pseudo-version below it")
	}
}

func TestOrdering(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.0.8", "0.0.7", 1},
		{"0.0.7", "0.0.8", -1},
		{"0.0.8", "0.0.8", 0},
		{"v0.0.8", "0.0.8", 0}, // a tag and a version are the same thing
		{"0.1.0", "0.0.99", 1},
		{"1.0.0", "0.99.99", 1},
		// §11.3: a prerelease is lower than the release it precedes.
		{"1.0.0-rc.1", "1.0.0", -1},
		// §11.4: identifiers compare left to right; numeric ones numerically,
		// which is where a string comparison gets it wrong.
		{"1.0.0-rc.2", "1.0.0-rc.10", -1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		// A numeric identifier ranks below an alphanumeric one.
		{"1.0.0-1", "1.0.0-alpha", -1},
		// A longer prerelease with an equal prefix ranks higher.
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		// §10: build metadata takes no part in ordering.
		{"1.0.0+build.9", "1.0.0+build.1", 0},
	} {
		got, err := Compare(c.a, c.b)
		if err != nil {
			t.Errorf("Compare(%q, %q): %v", c.a, c.b, err)
			continue
		}
		if got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// `internal/build` reports a bare revision, or the word "devel", for a binary
// with no release behind it, and chose those over a number precisely so that
// nothing would compare them. Honour that: a build nothing can place is not
// out of date, it is unplaceable, and telling its operator to upgrade would
// be telling them so forever.
func TestABuildWithNoReleaseBehindItCannotBeCompared(t *testing.T) {
	for _, v := range []string{"devel", "ed07e4883c86", "ed07e4883c86-dirty", "", "1.2", "1.2.3.4"} {
		if Comparable(v) {
			t.Errorf("%q is not a version that can be ordered against a release", v)
		}
		if _, err := Newer("0.0.9", v); err == nil {
			t.Errorf("comparing against %q should be an error, not an answer", v)
		}
	}
	for _, v := range []string{"0.0.8", "v0.0.8", "0.0.8-0.20260922110423-ed07e4883c86"} {
		if !Comparable(v) {
			t.Errorf("%q is a version and should be comparable", v)
		}
	}
}

// A leading zero is not a semver number, and accepting one would let "0.0.08"
// order differently from the "0.0.8" it looks like.
func TestALeadingZeroIsNotANumber(t *testing.T) {
	if Comparable("0.0.08") {
		t.Error("0.0.08 is not a valid version")
	}
	if !Comparable("0.0.0") {
		t.Error("0.0.0 is a valid version; a single zero is not a leading zero")
	}
}
