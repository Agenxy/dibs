// Command signstable fails an install that would revoke the operator's
// Files-and-Folders permission.
//
// macOS keys a TCC grant to a program's DESIGNATED REQUIREMENT: the expression
// codesign emits under `-r-`, which for a properly signed binary reads
// `identifier "org.agenxy.dibs" and certificate leaf = H"..."`. Rebuild that
// binary with the same identifier and the same signing identity and the
// requirement is unchanged, so the grant still applies and nobody is asked
// anything. Change either, and the grant silently stops matching and the
// operator is asked for access again on a dialog that explains none of this.
//
// THE FIRST VERSION OF THIS TOOL CHECKED THE WRONG THING, and the mistake is
// worth keeping written down because it is the more intuitive one. It compared
// the code-directory hash, which is a hash of the code and therefore changes
// whenever the code does. That is only the grant's key for AD-HOC signatures,
// where the requirement degrades to a bare `cdhash H"..."` and every build is a
// new program to TCC: the very failure the signing identity was introduced to
// end. Applied to a properly signed binary it fails every install that changed
// a line, which is all of them. It failed the install of the commit that added
// it, reporting the identity as broken when the identity was doing its job.
//
// A guard that cries wolf on every run is worse than no guard: it does not
// merely fail to catch the fault, it trains everyone to route around the check
// that would have. So this compares the requirement, which changes exactly when
// the operator's permission does.
//
// Ad-hoc builds are exempt only on a machine that never had an identity: it has
// no stable requirement to promise, and failing its install would be refusing to
// work rather than reporting a fault. Falling BACK to ad-hoc from a real
// identity is the opposite, and fails, because that is the reported fault
// happening rather than the absence of a promise.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// stampName is where the last install's requirements live, beside the binaries
// they describe, so a stamp cannot be separated from what it is a stamp of.
const stampName = ".dibs-codesign-stamp.json"

func main() {
	dest := os.Getenv("DIBS_INSTALL_DEST")
	if dest == "" && len(os.Args) > 1 {
		dest = os.Args[1]
	}
	if dest == "" {
		return // nothing named, nothing to check
	}
	// The destination is the install prefix the Taskfile passes. Constrained
	// rather than annotated away: absolute, cleaned, and required to exist as a
	// directory, so every path built from it below is a known-shaped join of a
	// validated prefix and a constant filename.
	dest = filepath.Clean(dest)
	if !filepath.IsAbs(dest) {
		fmt.Fprintf(os.Stderr, "signstable: install destination must be absolute, got %q\n", dest)
		os.Exit(1)
	}
	if fi, err := os.Stat(dest); err != nil || !fi.IsDir() {
		return // nothing installed there yet; nothing to promise
	}
	if err := check(dest); err != nil {
		fmt.Fprintf(os.Stderr, "signstable: %v\n", err)
		os.Exit(1)
	}
}

// designated matches the requirement line codesign prints under `-r-`. This,
// not the code-directory hash, is what TCC stores against a grant.
//
// The leading `# ` is not optional decoration: codesign comments the line out
// exactly when the requirement is IMPLICIT, which is the ad-hoc case, printing
// `# designated => cdhash H"..."`. A pattern anchored to a bare `designated`
// therefore matches every properly signed binary and silently skips every
// ad-hoc one, so the tool saw nothing at all in the one state it exists to
// report. It read as a clean pass.
var designated = regexp.MustCompile(`(?m)^#?\s*designated =>\s*(.+)$`)

func check(dest string) error {
	now, adhoc, ok := requirements(dest)
	if !ok || len(now) == 0 {
		return nil
	}
	return compare(dest, now, adhoc)
}

// requirements reads the designated requirement of each installed binary.
// ok is false when there is no signature to promise anything about, which is
// every non-macOS machine.
func requirements(dest string) (reqs map[string]string, adhoc, ok bool) {
	now := map[string]string{}
	for _, bin := range []string{"dibd", "dibs"} {
		// #nosec G703 -- `dest` was validated absolute in main; `bin` is a
		// constant from the list above.
		path := filepath.Join(dest, bin)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		// #nosec G204,G702 -- codesign is a literal; path is a validated absolute
		// directory joined to a constant filename, and exec passes argv directly
		// so nothing is interpreted by a shell.
		out, err := exec.Command("codesign", "-d", "-r-", path).CombinedOutput()
		if err != nil {
			// Deliberately not an error to the caller. An unsigned binary, or a
			// machine with no codesign at all, has no signature to keep stable, so
			// there is nothing here to promise and nothing to fail an install
			// over. Failing would refuse to install on Linux.
			return nil, false, false
		}
		m := designated.FindStringSubmatch(string(out))
		if m == nil {
			continue
		}
		req := strings.TrimSpace(m[1])
		// A requirement that names a cdhash is the ad-hoc case: every build is a
		// different program to TCC, so there is no stable requirement here.
		if strings.Contains(req, "cdhash") {
			adhoc = true
		}
		now[bin] = req
	}
	return now, adhoc, true
}

// compare holds the stamp against what is installed now, and rewrites it.
func compare(dest string, now map[string]string, adhoc bool) error {
	// #nosec G703 -- validated absolute prefix joined to a constant filename.
	stamp := filepath.Join(dest, stampName)
	prev := map[string]string{}
	if b, err := os.ReadFile(stamp); err == nil { // #nosec G304 -- a path this tool wrote
		_ = json.Unmarshal(b, &prev)
	}
	// Ad-hoc is exempt only on a machine that was never signed properly.
	//
	// Falling BACK to ad-hoc from a real identity is not the absence of a
	// promise, it is the promise breaking: the operator's grants were keyed to
	// the old requirement and have just stopped matching, and from here every
	// build asks them for access again. That is the reported fault itself, so
	// it is the last thing that should be waved through. The comparison below
	// catches it, because a stamped `identifier ...` no longer equals a cdhash
	// requirement.
	if adhoc && !signedBefore(prev) {
		return nil
	}
	var moved []string
	for bin, req := range now {
		// A stamp written by the cdhash version of this tool is a bare hex
		// string, not a requirement, and comparing the two would fail the first
		// install after the fix for a change that did not happen. Ignored once;
		// the stamp is rewritten below in the new form.
		if was, ok := prev[bin]; ok && was != req && strings.Contains(was, "identifier") {
			moved = append(moved, fmt.Sprintf("%s:\n    was: %s\n    now: %s", bin, was, req))
		}
	}
	b, err := json.MarshalIndent(now, "", "  ")
	if err != nil {
		return err
	}
	// #nosec G703 -- `stamp` is the constant filename above under a validated
	// absolute directory.
	if err := os.WriteFile(stamp, b, 0o600); err != nil {
		return err
	}
	if len(moved) > 0 {
		return fmt.Errorf("the designated requirement changed since the last install:\n  %s\n\n"+
			"  macOS keys your Desktop/Documents permission to that requirement, so it has\n"+
			"  just stopped matching and you will be asked for access again. Rebuilding does\n"+
			"  NOT do this: the requirement names the identifier and the signing certificate,\n"+
			"  and neither changes when the code does. Something is wrong with the identity:\n"+
			"    security find-identity -v -p codesigning   (should list \"Dibs Local Codesign\")\n"+
			"    go run ./tools/signid                      (should print the same name)",
			strings.Join(moved, "\n  "))
	}
	return nil
}

// signedBefore reports whether a previous install recorded a real signing
// identity, which is what makes a fall back to ad-hoc a regression rather than
// the ordinary state of a machine that has none.
func signedBefore(prev map[string]string) bool {
	for _, req := range prev {
		if strings.Contains(req, "identifier") && !strings.Contains(req, "cdhash") {
			return true
		}
	}
	return false
}
