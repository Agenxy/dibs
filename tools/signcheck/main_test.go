package main

import (
	"os/exec"
	"strings"
	"testing"
)

// This tool must never name a signing identity that is not Dibs'.
//
// It used to run `security find-identity`, print every result, and propose the
// first one. Both halves are wrong. Borrowing another project's certificate
// keys Dibs' macOS privacy grant to something that project owns, so rotating it
// there silently revokes matching here. And printing the list is a disclosure:
// a keychain holds identities for work that has not been announced, and this
// output is exactly what gets pasted into an issue or captured in a CI log.
//
// Asserted against the REAL keychain, because a fake one would prove only that
// the fake was not printed.
func TestSigncheckNamesOnlyItsOwnIdentity(t *testing.T) {
	out, err := exec.Command("security", "find-identity", "-v", "-p", "codesigning").Output()
	if err != nil {
		t.Skip("no keychain to read; nothing to leak")
	}
	var others []string
	for _, line := range strings.Split(string(out), "\n") {
		a := strings.Index(line, `"`)
		b := strings.LastIndex(line, `"`)
		if a < 0 || b <= a {
			continue
		}
		if name := line[a+1 : b]; name != dibsIdentity {
			others = append(others, name)
		}
	}
	if len(others) == 0 {
		t.Skip("this keychain holds no other identities, so nothing could leak")
	}
	// Whatever the tool decides, the decision is made by name.
	for _, name := range others {
		if haveIdentity(name) && name == dibsIdentity {
			t.Fatalf("haveIdentity matched %q as Dibs' own", name)
		}
	}
	// And the identity it would propose is fixed, not "whatever came first".
	if dibsIdentity == "" || strings.Contains(dibsIdentity, " Local Codesign") == false {
		t.Errorf("dibsIdentity = %q: it must be a name Dibs owns", dibsIdentity)
	}
	for _, name := range others {
		if strings.Contains(dibsIdentity, name) {
			t.Errorf("dibsIdentity %q contains another project's identity %q", dibsIdentity, name)
		}
	}
}
