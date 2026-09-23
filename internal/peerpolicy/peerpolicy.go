// Package peerpolicy answers one question about the socket wake route: if the
// daemon writes a notice to a Claude Code session's socket, will that session
// let the model see it?
//
// The route needs no configuration and it also fails closed by default, which
// is the combination that makes it worth a package of its own. Claude Code runs
// inbound peer messages through a `crossSessionInbound` setting: accept, hold
// or refuse. Left unset the rule is mode PARITY, and the clause that catches
// Dibs is the third one: a sender that asserts no permission-mode class of its
// own is held while the receiver bypasses permission prompts. Dibs is a daemon.
// It has no permission mode, so it asserts none, so it is held by every session
// running the mode an unattended fleet runs in.
//
// That reads backwards until you see what the check is for. Permission prompts
// are what stands between text arriving and an agent acting on it. A session in
// bypassPermissions has removed that check downstream, so the client moves it to
// the door: the one mode where unattested text would be acted on unseen is the
// one mode that will not admit it. A prompting session takes the same bytes
// without complaint.
//
// WHY THIS IS NOT JUST A DOC NOTE. Dibs described the hold correctly in four
// places and then stopped there, one of them saying outright that no message a
// sender can construct changes it. True about the wire and false about the
// situation: an explicit setting always beats the parity default, so the
// receiving operator turns the route on with one line. A warning that names a
// problem and not its remedy is half a diagnostic; a warning that keeps naming
// a remedy after it has been applied is worse, because the next real one reads
// like more of the same. This package is what lets `dibs doctor` tell those
// three states apart.
//
// Measured 2026-09-23 against Claude Code 2.1.280, twice each way. Two headless
// sessions, both --permission-mode bypassPermissions, the same frame written to
// each session socket: the default one answered peer_message_hold with cause
// "no-mode-asserted" and started no turn, the one with accept started a turn
// carrying a kind:"peer" origin. The setting is re-read per decision rather than
// cached at startup, so a running session picks up a change to the file within
// seconds and no restart is needed: measured by flipping a live session from
// accept to hold and watching the next delivery park with cause
// "explicit-setting".
//
// READING NEEDS NO PRIVILEGE AND CHANGING IT IS NOT OURS. Admitting unattested
// peer text is the receiving human's decision about their own machine, so this
// package reports and never writes, the same way internal/appfirewall prints a
// firewall fix it will not run.
package peerpolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Verdict is what a Claude Code session will do with a peer message from a
// sender that asserts no permission mode, which is every message Dibs sends.
type Verdict int

const (
	// Parity is the default: no setting decides it, so the session's own
	// permission mode does. A session that bypasses permission prompts HOLDS
	// the notice for its human; a session that prompts accepts it. Dibs cannot
	// tell which from outside the session, so this is a verdict about the
	// configuration and not a prediction about a delivery.
	Parity Verdict = iota
	// Accept means every session reading these settings admits the notice,
	// whatever mode it is in.
	Accept
	// Hold means the notice is parked for that human's approval. In a session
	// with no approval surface the park is not indefinite: since 2.1.280 it
	// arms the user-dialog deadline, five minutes by default, and then expires.
	Hold
	// Refuse means the session has opted out of cross-session messages
	// entirely.
	Refuse
)

// strictness orders the three real values so a tightening source can be
// compared against what is already decided.
//
// PARITY SITS AT ACCEPT'S LEVEL HERE, and that is the client's own rule rather
// than a convenience. A repository may only tighten, and it is compared against
// "accept" when nothing above it has decided, so a checkout asking for "accept"
// with no user setting present changes NOTHING: the parity default stands.
// Ranking parity as looser than accept would have this report an open route on
// a machine where the route is held, which is the exact failure this package
// was written to end.
func strictness(v Verdict) int {
	switch v {
	case Hold:
		return 1
	case Refuse:
		return 2
	default:
		return 0
	}
}

// Source is one settings file in Claude Code's own precedence order.
type Source struct {
	// Name is how the setting's owner would recognise the file, for a hint
	// that has to tell somebody where to go.
	Name string
	// Path is the file, absent or unreadable being an ordinary answer of none.
	Path string
	// Tightening marks the sources that may only make the policy stricter.
	// A repository cannot loosen what a user or an administrator decided,
	// which matters for the hint: telling somebody to set "accept" in their
	// own settings is wrong advice when a checkout is what is holding.
	Tightening bool
}

// Sources lists the files that decide this, strictest owner first.
//
// The order is Claude Code's, not ours, and getting it wrong would produce a
// confident hint pointing at a file that does not decide anything. Managed
// policy wins outright. The user's own settings come next. A repository's
// settings are applied afterwards and may only tighten, which is why they are
// marked rather than merely ordered.
func Sources(home, cwd string) []Source {
	out := []Source{
		{Name: "your organization's managed settings", Path: managedPath(), Tightening: false},
		{Name: "your own settings", Path: filepath.Join(home, ".claude", "settings.json"), Tightening: false},
	}
	if cwd != "" {
		out = append(out,
			Source{
				Name: "this checkout's local settings", Tightening: true,
				Path: filepath.Join(cwd, ".claude", "settings.local.json"),
			},
			Source{
				Name: "this checkout's settings", Tightening: true,
				Path: filepath.Join(cwd, ".claude", "settings.json"),
			},
		)
	}
	return out
}

// Read reports the effective verdict and which file decided it, or "" when
// nothing did.
func Read(home, cwd string) (Verdict, string) {
	srcs := Sources(home, cwd)
	vals := make([]string, len(srcs))
	for i, s := range srcs {
		vals[i] = valueIn(s.Path)
	}
	return decide(srcs, vals)
}

// decide is the whole rule, kept pure so it can be tested against the cases
// that actually cost something: an administrator forbidding what a user allows,
// a checkout tightening what a user allows, and a checkout that asks to loosen
// and is ignored.
//
// One source is missing and cannot be otherwise: Claude Code also consults a
// flag-provided settings layer between policy and the user, which arrives on a
// session's command line rather than from a file. A board cannot read it, so a
// verdict here is about the files, and a session started with an override of
// its own will not match. Said out loud because the alternative is a confident
// answer that is sometimes wrong with nothing marking which times.
func decide(srcs []Source, vals []string) (Verdict, string) {
	cur, set, where := Accept, false, ""
	for i, s := range srcs {
		got, ok := parse(vals[i])
		if !ok {
			continue
		}
		if s.Tightening {
			base := Accept
			if set {
				base = cur
			}
			if strictness(got) > strictness(base) {
				cur, set, where = got, true, s.Name
			}
			continue
		}
		if !set {
			cur, set, where = got, true, s.Name
		}
	}
	if !set {
		return Parity, ""
	}
	return cur, where
}

// parse maps the three values Claude Code accepts. Anything else is not a
// value this understands, and guessing at it would be a claim about somebody
// else's schema that this package has no way to check.
func parse(s string) (Verdict, bool) {
	switch s {
	case "accept":
		return Accept, true
	case "hold":
		return Hold, true
	case "refuse":
		return Refuse, true
	}
	return Accept, false
}

// valueIn reads one settings file. A missing file, a malformed one and an
// absent key are the same answer here: this file does not decide.
func valueIn(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path) // #nosec G304 -- a settings path this package composed
	if err != nil {
		return ""
	}
	var f struct {
		CrossSessionInbound string `json:"crossSessionInbound"`
	}
	if json.Unmarshal(b, &f) != nil {
		return ""
	}
	return f.CrossSessionInbound
}

// managedPath is the administrator's file, which is per-platform and is read
// rather than assumed to be absent: a managed Mac is exactly the machine where
// telling somebody to edit their own settings would waste their afternoon.
func managedPath() string {
	switch osName() {
	case "darwin":
		return "/Library/Application Support/ClaudeCode/managed-settings.json"
	case "windows":
		return `C:\ProgramData\ClaudeCode\managed-settings.json`
	default:
		return "/etc/claude-code/managed-settings.json"
	}
}

// osName is runtime.GOOS behind a variable so the per-platform path can be
// tested from one machine.
var osName = func() string { return runtime.GOOS }
