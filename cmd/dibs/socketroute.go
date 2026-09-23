package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/peerpolicy"
)

// reportSocketRoute says what the one route this board has will actually do.
//
// TWO FIXES, AND THE CHEAP ONE USED TO BE MISSING, AND THEN IT NAGGED.
//
// This said what was wrong and named only the expensive remedy, which is the
// failure the honesty rule exists to prevent: a hint has to name the corrective
// call, and "configure a command for every harness on the board" is not the
// only one. The hold is a DEFAULT. Claude Code holds a peer message whose
// sender asserts no permission-mode class only while the receiver bypasses
// prompts, and an explicit crossSessionInbound beats that default.
//
// Naming the remedy then created the second half of the problem, which is why
// this reads the setting rather than printing advice unconditionally. A warning
// that keeps demanding a fix after the fix is in place trains an operator to
// skim it, and the next real warning arrives looking like more of the same.
// So: open says so and stays quiet about repairs, held names the file that is
// holding, and refused says that no local change will help.
//
// Printed and never written, because accepting unattested peer text is the
// receiving human's call about their own machine, the same way
// internal/appfirewall prints a firewall fix it will not run.
func reportSocketRoute(dir string, ok reportFn, warn fixFn) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	// The CHECKOUT is not this board's data directory and must not be guessed
	// at: a repository may tighten the policy, and which repository depends on
	// where a session runs rather than on where the daemon keeps its ledger.
	// Passing "" asks peerpolicy to answer for the sources a board can honestly
	// read, which is what a diagnostic about a machine should do.
	verdict, where := peerpolicy.Read(home, "")
	const noReceipt = "The socket route sends no receipt either way, so a delivery " +
		"is still not confirmable: [wake.exec] is the route this daemon watches."
	command := "add a [wake.exec.<harness>] block to " + filepath.Join(dir, "dibs.toml")

	switch verdict {
	case peerpolicy.Accept:
		ok("the session socket route is open (" + where + ` set "crossSessionInbound" to "accept")`)
		warn("no wake command is configured, so no wake this daemon can confirm",
			"the socket route will reach a Claude Code session on this machine, which is "+
				"the common case and is why this is a warning rather than a problem. "+
				noReceipt+" To have one, "+command)
	case peerpolicy.Hold:
		warn("no wake command is configured, and the socket route is held",
			where+` sets "crossSessionInbound" to "hold", so a notice is parked for `+
				"approval and expires unseen in a session with nobody at it. Change "+
				`that to "accept" there, or `+command+" for a route this daemon can confirm")
	case peerpolicy.Refuse:
		warn("no wake command is configured, and the socket route is refused",
			where+` sets "crossSessionInbound" to "refuse", so sessions on this machine `+
				"take no peer messages at all and nothing local will change that. "+
				strings.ToUpper(command[:1])+command[1:]+", which is a route that does not "+
				"depend on the receiving session's inbox")
	default:
		warn("no wake command is configured, so the only route is best effort",
			"the harness session socket needs no setup and is tried first, but the "+
				"receiving session decides whether to accept a peer message and sends "+
				"no receipt: a Claude Code session in bypassPermissions mode HOLDS it "+
				"for its human, which is what an unattended fleet runs in. Nothing "+
				"will report a wake that was held. Two ways out, and you can take "+
				"both. "+strings.ToUpper(command[:1])+command[1:]+" for a route this "+
				"daemon can confirm; or open the socket route on the receiving side "+
				`by setting "crossSessionInbound": "accept" in ~/.claude/settings.json, `+
				"which still gives no receipt and lets any local process that can read "+
				"a session's peer key put a line in front of that agent")
	}
}
