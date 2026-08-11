//go:build dibdev

package humanauth

import "testing"

// The mock reaches every verdict, including the two nobody sees by accident.
//
// Declined and Unavailable are the reason the mock is scripted rather than a
// plain "pretend yes": they are the branches where the panel has to say
// something different and correct, and on a Mac with a working sensor there is
// no way to reach Unavailable at all.
func TestTheMockProducesEachVerdict(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want Verdict
	}{
		{"verified", Verified},
		{"declined", Declined},
		{"unavailable", Unavailable},
	} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(mockEnv, tc.env)
			got, err := Check(t.Context(), "probe")
			if got != tc.want {
				t.Errorf("verdict = %v, want %v", got, tc.want)
			}
			if err != nil {
				t.Errorf("err = %v, want nil: a scripted verdict is not a failure", err)
			}
			if !Mocked() {
				t.Error("Mocked() is false while a mock verdict is in force: callers " +
					"would present this as a real check")
			}
		})
	}
}

// A typo is not a verification.
//
// The switch could have defaulted to Verified for convenience, since that is the
// value a developer wants nine times out of ten. It does not: an unrecognised
// value falls through to the real sensor. Otherwise DIBS_PRESENCE_MOCK=ture
// would silently assert that somebody was sitting there, which is the failure
// this whole package is built to prevent: arriving, of all ways, by
// misspelling.
func TestAnUnrecognisedMockValueIsNotAVerification(t *testing.T) {
	t.Setenv(mockEnv, "ture")
	if Mocked() {
		t.Fatal("a misspelt value engaged the mock")
	}
	if verdict, _ := Check(t.Context(), "probe"); verdict == Verified {
		t.Fatal("a misspelt value produced Verified")
	}
}

// An unset variable leaves a dev build behaving exactly like a release one, so
// the tag alone never changes what a developer sees at the sensor.
func TestAnUnsetVariableLeavesTheRealPathAlone(t *testing.T) {
	t.Setenv(mockEnv, "")
	if Mocked() {
		t.Fatal("the mock engaged with no value set: merely building with the dev " +
			"tag would then stop checking anybody")
	}
}
