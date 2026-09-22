package appfirewall

import (
	"strings"
	"testing"
)

// The shape of `socketfilterfw --listapps`, copied from a real machine: a count
// line, then numbered path lines each followed by an indented parenthetical.
// The trailing spaces on the path lines are the tool's own and are kept, since
// stripping them here would hide a parser that cannot cope with them.
const listApps = `Total number of apps = 3 
1 : /usr/sbin/sshd 
	 (Allow incoming connections) 
2 : /Users/dr.marbles/.local/bin/quic-probe 
	 (Allow incoming connections) 
3 : /Applications/Some Blocked App.app 
	 (Block incoming connections) 
`

// TestAHubOnAFilteredMachineIsReportedBlocked is the case this package was
// written for and the one that cost an afternoon: dibd listening on a LAN
// address, the firewall on with its default settings, and dibd not in the list.
// Nothing refuses the connection, so every symptom points at the network.
func TestAHubOnAFilteredMachineIsReportedBlocked(t *testing.T) {
	got := verdict("Firewall is enabled. (State = 1)",
		"Firewall has block all state set to disabled.",
		listApps, "/Users/dr.marbles/.local/bin/dibd")
	if got != Blocked {
		t.Fatalf("a daemon absent from the firewall list is Blocked, got %v", got)
	}
}

func TestAListedDaemonIsAllowed(t *testing.T) {
	got := verdict("Firewall is enabled. (State = 1)",
		"Firewall has block all state set to disabled.",
		listApps, "/Users/dr.marbles/.local/bin/quic-probe")
	if got != Allowed {
		t.Fatalf("a daemon the firewall permits is Allowed, got %v", got)
	}
}

func TestAnExplicitlyBlockedDaemonIsBlocked(t *testing.T) {
	got := verdict("Firewall is enabled. (State = 1)",
		"Firewall has block all state set to disabled.",
		listApps, "/Applications/Some Blocked App.app")
	if got != Blocked {
		t.Fatalf("an entry reading Block incoming connections is Blocked, got %v", got)
	}
}

// A firewall that is off filters nothing, and saying anything about the list in
// that state sends an operator to fix something that is not in the way.
func TestAFirewallThatIsOffIsNotAVerdictAboutTheList(t *testing.T) {
	got := verdict("Firewall is disabled. (State = 0)",
		"Firewall has block all state set to disabled.",
		listApps, "/Users/dr.marbles/.local/bin/dibd")
	if got != Off {
		t.Fatalf("State = 0 is Off whatever the list says, got %v", got)
	}
}

// Block-all outranks an allow entry. Reporting Allowed here would be the worst
// answer available: the operator has a permission that is not being honoured
// and a report telling them to look somewhere else.
func TestBlockAllOutranksAnAllowEntry(t *testing.T) {
	got := verdict("Firewall is enabled. (State = 2)",
		"Firewall has block all state set to enabled.",
		listApps, "/usr/sbin/sshd")
	if got != Blocked {
		t.Fatalf("block-all blocks a listed app too, got %v", got)
	}
}

// An entry whose verdict line is missing says nothing, and nothing is not
// permission. This is the shape a truncated or reformatted listing takes, and
// the honest reading of it is the one that makes the operator look.
func TestAnEntryWithNoVerdictLineIsNotPermission(t *testing.T) {
	got := verdict("Firewall is enabled. (State = 1)",
		"Firewall has block all state set to disabled.",
		"Total number of apps = 1 \n1 : /usr/local/bin/dibd \n", "/usr/local/bin/dibd")
	if got != Blocked {
		t.Fatalf("an entry with no following verdict is Blocked, got %v", got)
	}
}

// A path is compared whole. A suffix match would let any binary whose name ends
// the same way answer for this one, which is a permission check reading the
// wrong file: the class of bug this repository keeps finding in path handling.
func TestASimilarPathDoesNotAnswerForThisOne(t *testing.T) {
	list := "Total number of apps = 1 \n1 : /opt/elsewhere/dibd \n\t (Allow incoming connections) \n"
	if got := verdict("Firewall is enabled. (State = 1)", "", list, "/usr/local/bin/dibd"); got != Blocked {
		t.Fatalf("a different dibd does not grant this one clearance, got %v", got)
	}
}

// A state this code has never seen is not a verdict. Apple has changed these
// strings before, and a parser that reads an unknown state as "off" would
// silently stop reporting the very thing it exists for.
func TestAnUnrecognisedStateIsUnknown(t *testing.T) {
	if got := verdict("something Apple changed", "", listApps, "/usr/local/bin/dibd"); got != Unknown {
		t.Fatalf("an unparseable state is Unknown, got %v", got)
	}
}

// The fix is pasted into a shell by a person, so a path with a space in it has
// to survive the trip. sudoFix rather than Fix, because which of the two
// routes Fix chooses is a property of the machine running the test.
func TestTheFixSurvivesAPathWithASpace(t *testing.T) {
	got := sudoFix("/Users/dr marbles/.local/bin/dibd")
	want := "sudo " + firewallTool + " --add '/Users/dr marbles/.local/bin/dibd'" +
		" && sudo " + firewallTool + " --unblockapp '/Users/dr marbles/.local/bin/dibd'"
	if got != want {
		t.Fatalf("Fix quoted the path wrong:\n got %s\nwant %s", got, want)
	}
}

// A managed Mac gets a place to go, not a command that will be refused. This
// is the case the hub deployment actually hit: `socketfilterfw` answers every
// modifying verb with "Firewall settings cannot be modified from command line
// on managed Mac computers", and an operator handed a sudo line there is being
// sent in a circle on the machine where they are already stuck.
func TestAManagedMacIsGivenTheRouteThatExists(t *testing.T) {
	got := byHandFix("/Users/dr.marbles/.local/bin/dibd")
	if strings.Contains(got, "sudo") {
		t.Errorf("a managed Mac cannot use the command line for this; got %q", got)
	}
	for _, want := range []string{"System Settings", "Firewall", "/Users/dr.marbles/.local/bin/dibd"} {
		if !strings.Contains(got, want) {
			t.Errorf("the by-hand route has to name %q; got %q", want, got)
		}
	}
}
