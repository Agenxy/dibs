// Package appfirewall answers one question on macOS: if this process listens on
// an address other machines can route to, will their connections actually reach
// it?
//
// The macOS Application Firewall is on by default on a fresh install and filters
// INBOUND connections per executable. A binary that is not in its list is not
// refused: the kernel completes the TCP handshake and then hands the connection
// nowhere, so the listener's Accept never fires. From the client that is a
// connect() that succeeds followed by a read that hangs until the timeout, and
// from the server it is a daemon with an open listening socket, a healthy log
// and no traffic. Nothing in either process is wrong, which is why this cost an
// afternoon on the first machine Dibs was deployed to as a hub: `dibd up`
// printed a board URL, the port answered a TCP probe, and the board was
// unreachable from every other computer on the LAN.
//
// Normally the firewall asks the person at the keyboard whether to allow the
// program. A daemon installed over ssh has nobody to ask, so the dialog never
// appears and the default stands. That is the case this package exists for: the
// deployment that has no human in front of it is exactly the one that gets no
// warning.
//
// Detection needs no privilege. The FIX does, which is why this reports and
// never repairs: see Fix.
package appfirewall

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Verdict is what the firewall will do with an inbound connection to a binary.
type Verdict int

const (
	// Unknown is every case where no honest claim can be made: not macOS, or
	// socketfilterfw is missing or unreadable. A caller must say nothing rather
	// than guess, because both wrong answers are expensive: claiming a block
	// sends an operator to a firewall that is off, and claiming clearance
	// leaves them debugging the network.
	Unknown Verdict = iota
	// Off means the firewall is not filtering inbound connections at all.
	Off
	// Allowed means it is filtering, and this binary may accept them.
	Allowed
	// Blocked means it is filtering and this binary may not accept them.
	// Connections hang rather than being refused.
	Blocked
)

// firewallTool is Apple's own CLI for the Application Firewall. It is readable
// by any user; only its mutating verbs need root.
const firewallTool = "/usr/libexec/ApplicationFirewall/socketfilterfw"

// Check reports what the Application Firewall will do with inbound connections
// to binary, which must be a path to an executable on this machine.
func Check(binary string) Verdict {
	if runtime.GOOS != "darwin" || binary == "" {
		return Unknown
	}
	state, err := run("--getglobalstate")
	if err != nil {
		return Unknown
	}
	blockAll, err := run("--getblockall")
	if err != nil {
		// Older systems without the verb are still worth judging on the list.
		blockAll = ""
	}
	apps, err := run("--listapps")
	if err != nil {
		return Unknown
	}
	// The path the firewall records is the real file, not a symlink to it: an
	// install that puts dibd in ~/.local/bin via a link would otherwise never
	// match its own entry and report a block that is not there.
	if resolved, rerr := filepath.EvalSymlinks(binary); rerr == nil {
		binary = resolved
	}
	return verdict(state, blockAll, apps, binary)
}

// verdict is the whole decision, separated from the three commands that feed it
// so it can be tested against captured output from real machines. Every defect
// this package could have is in here; none of it is in the exec calls.
func verdict(globalState, blockAll, listApps, binary string) Verdict {
	// "Firewall is disabled. (State = 0)". Matching on the number rather than
	// the sentence, which is localised.
	if strings.Contains(globalState, "State = 0") {
		return Off
	}
	if !strings.Contains(globalState, "State = 1") && !strings.Contains(globalState, "State = 2") {
		return Unknown // a state this code has never seen is not a verdict
	}
	// Block-all outranks the list: with it set, an entry saying "allow" is not
	// honoured, and reporting Allowed on the strength of one would send an
	// operator looking at the network instead of at the switch that is on.
	if strings.Contains(blockAll, "enabled") && !strings.Contains(blockAll, "disabled") {
		return Blocked
	}
	return listVerdict(listApps, binary)
}

// listVerdict reads `socketfilterfw --listapps`, whose entries are a numbered
// path line followed by an indented parenthetical:
//
//	1 : /usr/sbin/sshd
//	         (Allow incoming connections)
//
// The decision therefore needs the line AFTER the match, and an entry with no
// following verdict line is treated as absent rather than as permission.
func listVerdict(listApps, binary string) Verdict {
	lines := strings.Split(listApps, "\n")
	for i, line := range lines {
		if !namesBinary(line, binary) {
			continue
		}
		if i+1 < len(lines) && strings.Contains(lines[i+1], "Allow incoming connections") {
			return Allowed
		}
		return Blocked
	}
	return Blocked // not listed is not permitted
}

// namesBinary reports whether a listapps entry line is about this executable.
// The line is "<n> : <path>" with trailing spaces, and a path is compared whole:
// a suffix match would let /opt/evil/dibd answer for /usr/local/bin/dibd.
func namesBinary(line, binary string) bool {
	_, path, found := strings.Cut(line, " : ")
	return found && strings.TrimSpace(path) == binary
}

// Fix is how this machine's operator clears the block, for printing rather
// than running.
//
// It needs root, and Dibs does not take root. A daemon that could edit the
// machine's firewall is a bigger thing than a coordination service, and an
// operator who is told exactly what to type keeps the decision the firewall
// exists to give them.
//
// On an MDM-managed Mac there is no command to type: socketfilterfw refuses
// every modifying verb with "Firewall settings cannot be modified from command
// line on managed Mac computers", so handing that operator a sudo line sends
// them to a command that cannot work, on the machine where they are already
// stuck. Managed Macs are the normal case for the always-on host somebody
// deploys a hub to, which is how this was found.
func Fix(binary string) string {
	if managed() {
		return byHandFix(binary)
	}
	return sudoFix(binary)
}

// sudoFix is the two commands for a Mac whose firewall the command line can
// still change.
func sudoFix(binary string) string {
	q := shellQuote(binary)
	return "sudo " + firewallTool + " --add " + q +
		" && sudo " + firewallTool + " --unblockapp " + q
}

// byHandFix is the route left on a managed Mac. It names the screen rather
// than a command, because there is no command.
func byHandFix(binary string) string {
	return "this Mac is MDM-managed and refuses firewall changes from the command " +
		"line, so it has to be done at the machine: System Settings > Network > " +
		"Firewall > Options, add " + binary + " and set it to allow incoming " +
		"connections. Starting the daemon from a logged-in desktop session raises " +
		"the same request as a dialog; over ssh there is nobody to show it to"
}

// managed reports whether this Mac refuses firewall changes from the command
// line, by asking the tool rather than by inferring it from MDM enrollment:
// enrollment is a fact about the fleet and this is a fact about the tool, and
// the tool states it unprompted. Its usage text is the cheapest verb that
// carries the refusal, and reading usage changes nothing.
func managed() bool {
	out, err := exec.Command(firewallTool, "-h").CombinedOutput() // #nosec G204 -- fixed argv
	if err != nil && len(out) == 0 {
		return false
	}
	return strings.Contains(string(out), "managed Mac")
}

// shellQuote makes a path safe to paste into a shell. Fix's output is read by a
// person and typed into their terminal, so a path with a space in it has to
// survive the trip; ~/Library/Application Support is an ordinary place for a
// binary to live on this platform.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func run(arg string) (string, error) {
	// #nosec G204 -- no shell, and arg is one of this file's own constants.
	out, err := exec.Command(firewallTool, arg).Output()
	return string(out), err
}
