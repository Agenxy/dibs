package main

import (
	"strings"
	"testing"
)

// The hub case, which is the one this was written for: a Mac that somebody
// logged into once and then left running. The LaunchAgent is in the `gui/501`
// domain, that domain exists only while the user is logged in, and nothing
// logs them in after a restart. Measured on the first machine Dibs was
// deployed to as a hub: `launchctl print gui/501/org.agenxy.dibs` names the
// domain, and `autoLoginUser` was not set.
func TestAMacWithNoAutomaticLoginComesBackAtLoginNotAtBoot(t *testing.T) {
	state, fix := macBootReturn("", "dr.marbles")
	if state != bootAtLogin {
		t.Fatalf("no automatic login means the unit waits for a login, got %v", state)
	}
	for _, want := range []string{"dr.marbles", "LaunchDaemon", "automatic login"} {
		if !strings.Contains(fix, want) {
			t.Errorf("the fix has to name %q; got %q", want, fix)
		}
	}
}

func TestAMacThatLogsThisUserInComesBackAtBoot(t *testing.T) {
	if state, _ := macBootReturn("dr.marbles", "dr.marbles"); state != bootAtBoot {
		t.Fatalf("automatic login for this user means the unit starts at boot, got %v", state)
	}
}

// The login window stores the short name as an administrator typed it, and
// macOS does not distinguish case in it. Comparing exactly would report an
// always-on Mac as needing a fix it has already applied, which is the kind of
// wrong answer that teaches people to ignore the report.
func TestTheAutomaticLoginNameIsComparedWithoutCase(t *testing.T) {
	if state, _ := macBootReturn("Dr.Marbles\n", "dr.marbles"); state != bootAtBoot {
		t.Fatalf("the recorded name differs only in case, got %v", state)
	}
}

// Automatic login for SOMEBODY ELSE is not automatic login for this unit:
// their session's launchd domain does not hold this user's LaunchAgent.
func TestAutomaticLoginForAnotherAccountDoesNotCount(t *testing.T) {
	if state, _ := macBootReturn("kiosk", "dr.marbles"); state != bootAtLogin {
		t.Fatalf("another account's session does not load this user's agent, got %v", state)
	}
}

func TestLingerIsTheSameQuestionOnLinux(t *testing.T) {
	if state, _ := linuxBootReturn("yes", "dibs"); state != bootAtBoot {
		t.Fatalf("Linger=yes starts the user manager at boot, got %v", state)
	}
	state, fix := linuxBootReturn("no", "dibs")
	if state != bootAtLogin {
		t.Fatalf("Linger=no means the unit waits for a session, got %v", state)
	}
	if !strings.Contains(fix, "enable-linger dibs") {
		t.Errorf("the fix has to be the command, with the account in it; got %q", fix)
	}
}

// A reading that failed is not a verdict. `loginctl` is absent on a machine
// with no systemd, and reporting "waits for login" there would send somebody
// to a command that does not exist.
func TestAnUnreadableAnswerIsNotAVerdict(t *testing.T) {
	if state, _ := linuxBootReturn("", "dibs"); state != bootUnknown {
		t.Fatalf("no loginctl answer is Unknown, got %v", state)
	}
	if state, _ := macBootReturn("", ""); state != bootUnknown {
		t.Fatalf("no account name is Unknown, got %v", state)
	}
}

// And the check stays quiet when there is no service installed, because there
// is then nothing that was supposed to come back.
func TestNoServiceMeansNothingToSayAboutReboots(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var said []string
	checkServiceReturnsAfterAReboot(
		func(s string) { said = append(said, "ok: "+s) },
		func(what, fix string) { said = append(said, "warn: "+what) },
	)
	if len(said) != 0 {
		t.Fatalf("no unit installed, so nothing to report; got %v", said)
	}
}
