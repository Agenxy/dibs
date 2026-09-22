package main

import (
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
)

// Whether the service comes back on its own after a reboot, which is a
// different question from whether it is installed.
//
// `dibs configure --service` writes a USER-scoped unit on both platforms: a
// launchd LaunchAgent in ~/Library/LaunchAgents, and a systemd unit in the
// user manager. Both are documented here and in the CLI's own help as making
// the daemon "survive a closed terminal and a reboot", and the first half is
// true. The second is true only when this user has a session at boot:
//
//   - a LaunchAgent lives in the `gui/<uid>` domain, which exists while that
//     user is logged in at the screen. `launchctl print` says so outright.
//     Without automatic login, a restarted Mac has no such domain and the job
//     is not loaded, let alone started.
//   - a systemd user manager is torn down when the user's last session ends
//     and is not started at boot unless lingering is enabled for them.
//
// So the machine this matters most on is the one it silently fails on: the
// always-on host somebody deploys a hub to, logged into once and then left.
// The board comes back from a crash, because KeepAlive and Restart handle
// that, and does not come back from a power cut. Nothing said so until now.
//
// This reports and does not repair. Automatic login weakens the machine, and
// a system-wide LaunchDaemon needs root; both are the operator's call, and
// both are cheap once somebody knows they have to be made.
type bootReturn int

const (
	// bootUnknown is every case with no honest answer: another platform, or a
	// tool that could not be read. Saying nothing beats guessing here, because
	// the wrong reassurance is what this check exists to remove.
	bootUnknown bootReturn = iota
	// bootAtBoot means this user has a session without anybody arriving, so
	// the unit starts with the machine.
	bootAtBoot
	// bootAtLogin means the unit waits for this user to log in.
	bootAtLogin
)

// bootReturnHere answers for this machine and this user, with the fix.
func bootReturnHere() (bootReturn, string) {
	switch runtime.GOOS {
	case "darwin":
		return macBootReturn(autoLoginUser(), currentUser())
	case "linux":
		return linuxBootReturn(lingerState(currentUser()), currentUser())
	default:
		return bootUnknown, ""
	}
}

// macBootReturn compares who macOS logs in by itself against who this is.
// Case-insensitively, because the login window records the short name as the
// administrator typed it and macOS does not distinguish.
func macBootReturn(autoLogin, me string) (bootReturn, string) {
	if me == "" {
		return bootUnknown, ""
	}
	if strings.EqualFold(strings.TrimSpace(autoLogin), me) {
		return bootAtBoot, ""
	}
	return bootAtLogin, "this is a LaunchAgent, so it is loaded when " + me +
		" logs in at the screen and not when the machine boots: after a restart the " +
		"board stays down until somebody sits at this Mac. That is fine on a laptop " +
		"and is not what an always-on host wants. Two ways to close it, both the " +
		"operator's call: turn on automatic login for " + me + " in System Settings > " +
		"Users & Groups (FileVault holds even that until someone unlocks the disk " +
		"once), or run dibd from a system LaunchDaemon in /Library/LaunchDaemons, " +
		"which starts at boot with no session at all and needs root to install"
}

// linuxBootReturn reads the same question off systemd, where it has a name.
func linuxBootReturn(linger, me string) (bootReturn, string) {
	switch {
	case me == "" || linger == "":
		return bootUnknown, ""
	case strings.EqualFold(linger, "yes"):
		return bootAtBoot, ""
	}
	return bootAtLogin, "this is a systemd USER unit, so it starts with a session for " +
		me + " and stops with the last one: after a reboot with nobody logging in, the " +
		"board stays down. `loginctl enable-linger " + me + "` starts the user manager " +
		"at boot and keeps it after logout, which is what an always-on host wants"
}

// autoLoginUser is the account macOS logs in by itself, or "" for none. The
// preference is world readable, so this needs no privilege.
func autoLoginUser() string {
	// #nosec G204 -- fixed argv, no shell.
	out, err := exec.Command("defaults", "read",
		"/Library/Preferences/com.apple.loginwindow", "autoLoginUser").Output()
	if err != nil {
		return "" // unset is an error exit from `defaults`, and unset is the answer
	}
	return strings.TrimSpace(string(out))
}

// lingerState is systemd's own word for the same property, or "" if it cannot
// be read.
func lingerState(me string) string {
	if me == "" {
		return ""
	}
	// #nosec G204 -- argv passed directly, no shell; me is this process's own
	// account name, not caller-supplied text.
	out, err := exec.Command("loginctl", "show-user", me, "--property=Linger").Output()
	if err != nil {
		return ""
	}
	_, value, found := strings.Cut(strings.TrimSpace(string(out)), "=")
	if !found {
		return ""
	}
	return value
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return strings.TrimSpace(os.Getenv("USER"))
}
