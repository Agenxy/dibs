//go:build !lanesdev

package humanauth

import "testing"

// The shipped build cannot be told that a human is present.
//
// This is the test the mock exists to be held to. It runs under the DEFAULT
// build (no tags) which is what `go test ./...`, `task ci`, and every release
// binary get, so the guarantee is the one that holds unless somebody goes out of
// their way, rather than the one that holds if they remember to.
//
// The variable is set here deliberately, to the most dangerous value, and the
// assertion is that it changes nothing. If a future refactor ever moves the mock
// behind a runtime check "for convenience": an env var, a config key, a debug
// flag: this test fails, and it fails on the exact claim that matters: software
// asserted that a person was present, and was believed.
func TestAReleaseBuildCannotBeToldAHumanIsPresent(t *testing.T) {
	t.Setenv("LANES_PRESENCE_MOCK", "verified")

	if Mocked() {
		t.Fatal("Mocked() is true in a build with no lanesdev tag: the mock reached " +
			"a shipped binary")
	}
	verdict, _ := Check(t.Context(), "probe")
	if verdict == Verified {
		t.Fatal("an environment variable produced a Verified presence check. " +
			"any agent able to set the environment can now speak as the operator")
	}
}
