package main

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/selfupdate"
)

// `dibs upgrade` is the one command that restarts a daemon a live fleet is
// talking to, so it refuses an argument it does not understand rather than
// guessing. The same rule has to cover a COMBINATION it does not understand:
// --check says "change nothing" and --fetch says "change something", and
// silently letting one win would make the safer of the two words meaningless.
func TestCheckAndFetchTogetherAreRefusedRatherThanRanked(t *testing.T) {
	err := upgradeCmd([]string{"--check", "--fetch"})
	if err == nil {
		t.Fatal("asking to both look and leap has to be refused, not resolved")
	}
	if !strings.Contains(err.Error(), "pick one") {
		t.Errorf("the refusal has to say what to do; got %q", err)
	}
}

// --allow-unsigned weakens the one check that says a binary about to be run
// as a daemon is the one this project published. On its own it does nothing,
// and a flag that does nothing is one somebody believes they have used.
func TestAllowUnsignedWithoutFetchIsRefused(t *testing.T) {
	err := upgradeCmd([]string{"--allow-unsigned"})
	if err == nil {
		t.Fatal("a flag with nothing to apply to must say so")
	}
	if !strings.Contains(err.Error(), "--fetch") {
		t.Errorf("the refusal has to name the flag it belongs with; got %q", err)
	}
}

// An unknown argument is still refused, which this keeps true now that the
// switch has four more cases to fall through.
func TestAnUnknownArgumentIsStillRefused(t *testing.T) {
	if err := upgradeCmd([]string{"--fetch-it-now"}); err == nil {
		t.Fatal("an argument this command does not understand restarts a daemon by guess")
	}
}

// The operator's way to keep `dibs doctor` off the network entirely. A
// diagnostic is what somebody reaches for when the network may be the thing
// that is wrong, and one that cannot be told to stop asking is one they
// cannot use.
func TestTheUpdateCheckCanBeTurnedOff(t *testing.T) {
	t.Setenv(selfupdate.SkipEnv, "1")
	var said []string
	reportUpdate(
		func(s string) { said = append(said, "ok: "+s) },
		func(what, fix string) { said = append(said, "warn: "+what+" / "+fix) },
	)
	if len(said) != 0 {
		t.Fatalf("%s=1 means no request and no line; got %v", selfupdate.SkipEnv, said)
	}
	// And the values that mean "no" are not an accidental yes: an operator who
	// writes 0 or false has said the same thing as not setting it.
	for _, off := range []string{"0", "false", "FALSE", ""} {
		t.Setenv(selfupdate.SkipEnv, off)
		if selfupdate.Skipped() {
			t.Errorf("%q does not mean the check is off", off)
		}
	}
}
