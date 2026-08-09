//go:build lanesdev

package humanauth

import "os"

// This file is compiled only under `-tags lanesdev`. See mock_release.go for why
// the switch is a build tag and not a runtime check.
//
// It exists because the human flow is otherwise undrivable without a person: no
// test, no CI run, and no unattended development session can produce a
// fingerprint. Every branch of the flow needs exercising — the panel says three
// different things for Verified, Declined and Unavailable, and the two failure
// sentences are the ones nobody ever sees by accident.

// mockEnv scripts the verdict. Unset, or set to anything unrecognised, means no
// mock: an unknown value falls through to the real sensor rather than being
// treated as a verification, because a typo must not be a way to assert that
// somebody is present.
const mockEnv = "LANES_PRESENCE_MOCK"

func mocked() (Verdict, bool) {
	switch os.Getenv(mockEnv) {
	case "verified":
		return Verified, true
	case "declined":
		return Declined, true
	case "unavailable":
		return Unavailable, true
	}
	return Unavailable, false
}

// Mocked reports whether presence is being answered by a script. Callers surface
// it so a mocked run is never mistaken for evidence that the real path works.
func Mocked() bool {
	_, on := mocked()
	return on
}
