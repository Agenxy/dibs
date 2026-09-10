package notify

import (
	"strings"
	"testing"
)

// ON A PLATFORM WITH NO NOTIFIER, SAY WHERE APPROVALS GO.
//
// Reach said "this platform has no notification route", which is true and
// tells an operator nothing about the consequence: a request that needs them
// waits, on the board, until they go and look. Being asked and answering is
// how a person stays the authority over a fleet (#63), so on the platform
// where the asking half is absent the one place it still works has to be
// named, and so does the fact that nothing will interrupt them.
func TestReachOnLinuxSaysApprovalsWaitOnTheBoard(t *testing.T) {
	t.Setenv(silenceEnv, "")
	old := goos
	goos = "linux"
	t.Cleanup(func() { goos = old })

	ok, why := Reach()
	if ok {
		t.Fatal("Reach claims notifications reach a person on linux, where no notifier exists")
	}
	for _, want := range []string{"linux", "ASK", "dibs web", "#63"} {
		if !strings.Contains(why, want) {
			t.Errorf("the explanation does not mention %q, which is the part an "+
				"operator acts on:\n  %s", want, why)
		}
	}
}
