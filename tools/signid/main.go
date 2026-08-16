// Command signid prints the code-signing identity `task install` should use.
//
// It exists because the fix for macOS revoking a privacy grant on every install
// only worked if somebody remembered an environment variable.
//
// macOS keys a Files-and-Folders grant to a program's code signature. The Go
// toolchain signs ad-hoc, so every rebuild is a different program and the grant
// stops applying: you allow it, install again, and are asked again, with nothing
// connecting the two. tools/signcheck exists to say so and names the remedy, a
// self-signed certificate called "Dibs Local Codesign". Having made one, the
// operator then had to pass DIBS_CODESIGN_IDENTITY on every install forever, and
// an install that forgot it silently went back to ad-hoc and revoked the grant
// again. A fix conditional on remembering is the failure mode this repository
// keeps writing guards against, so it is not one.
//
// Measured on the machine this was written on: nine installs in one session,
// nine prompts, and the operator asking why. The warning printed nine times and
// was never read.
//
// So: the environment variable still wins, because an operator naming an
// identity means it. Otherwise the canonical name is used IF it is in the
// keychain. Otherwise "-", which is ad-hoc and exactly what happened before.
//
// A Go tool rather than a shell conditional in the Taskfile, per CONTRIBUTING:
// this is a decision with three branches and a failure mode that is invisible
// when it goes wrong, which is precisely what should not live in untyped glue.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Identity is the only name this tool will look for, and it must match
// tools/signcheck, which is what tells the operator to create it.
//
// Never another project's identity, however convenient. A grant keyed to a
// certificate some unrelated project owns is silently revoked when they rotate
// or delete it, which is a cross-project dependency established by accident.
const Identity = "Dibs Local Codesign"

// adhoc is codesign's spelling of "no identity".
const adhoc = "-"

func main() {
	if v := strings.TrimSpace(os.Getenv("DIBS_CODESIGN_IDENTITY")); v != "" {
		fmt.Print(v)
		return
	}
	if inKeychain(Identity) {
		fmt.Print(Identity)
		return
	}
	fmt.Print(adhoc)
}

// inKeychain reports whether the named identity is present and usable for code
// signing.
//
// By NAME, never by position. `security` orders identities by keychain, and
// "whatever is first" is how another project's certificate once got proposed.
//
// Quoted, so a substring cannot match: an identity called "Dibs Local Codesign
// (revoked)" is not this one.
func inKeychain(name string) bool {
	out, err := exec.Command("security", "find-identity", "-v", "-p", "codesigning").Output()
	if err != nil {
		// No keychain, no `security`, or not macOS. Ad-hoc is the honest answer
		// and the install still works; it is what happened before this existed.
		return false
	}
	return strings.Contains(string(out), `"`+name+`"`)
}
