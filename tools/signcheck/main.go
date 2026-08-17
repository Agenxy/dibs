// Command signcheck refuses an install that would silently revoke a macOS
// privacy grant.
//
// macOS keys a Files-and-Folders or Full Disk Access grant to a program's code
// signature. The Go toolchain signs ad-hoc, so every rebuild is a different
// program to the system and the grant stops applying. When checkouts live under
// Desktop, Documents or Downloads, that grant is the difference between
// work-overlap matching running and matching being off.
//
// The failure is invisible and repeats: install, get prompted, allow, install
// again, get prompted again, with nothing connecting the two. It happened twice
// in one session on the machine this was written on, the second time
// immediately after the person had been told it would.
//
// So this stops the install rather than warning into a scrollback that nobody
// reads. It stops ONLY when all three are true: macOS, a tree that actually
// needs the grant, and a usable identity already in the keychain. Anything else
// proceeds, because refusing an install nobody can satisfy is worse than the
// prompt.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func main() { run() }

// run REPORTS; it no longer refuses.
//
// It used to stop an install that would have signed ad-hoc while a usable
// identity sat unused in the keychain, which was the right call when using one
// meant naming it in an environment variable. tools/signid resolves it by name
// now, so that situation cannot arise: if the identity exists, the install uses
// it. What is left is the case signid cannot fix, which is having no identity at
// all, and the answer to that is a certificate this machine does not have yet
// rather than a refusal.
func run() {
	if runtime.GOOS != "darwin" || os.Getenv("DIBS_CODESIGN_IDENTITY") != "" {
		return
	}
	if os.Getenv("DIBS_ADHOC_OK") == "1" {
		fmt.Println("signcheck: ad-hoc install allowed by DIBS_ADHOC_OK=1")
		return
	}
	guarded := protectedPaths()
	if len(guarded) == 0 {
		return // nothing here needs the grant
	}
	if !haveIdentity(dibsIdentity) {
		// No identity of Dibs' own. Say what will happen, and do not block: the
		// fix is a certificate this machine does not have yet.
		fmt.Fprintf(os.Stderr,
			"signcheck: %s is in a macOS protected folder and dibd will be ad-hoc signed,\n"+
				"  so any Desktop/Documents/Downloads permission you grant is revoked by the\n"+
				"  next install.\n\n"+
				"  To keep the grant, give Dibs a signing identity of its own: Keychain Access →\n"+
				"  Certificate Assistant → Create a Certificate, named %q, type Code\n"+
				"  Signing, self-signed. `task install` finds it by name after that.\n",
			guarded[0], dibsIdentity)
		return
	}
	// The identity EXISTS, so the install is not ad-hoc: tools/signid resolves it
	// by name with no variable set, which is the whole point of that tool.
	//
	// This used to refuse here, telling the operator to set
	// DIBS_CODESIGN_IDENTITY, which was correct when the only way to use an
	// identity was to name it and is now simply false. Two tools that decide the
	// same thing have to decide it the same way; this one was left behind by the
	// other and turned a solved problem into a blocked install.
	fmt.Fprintf(os.Stderr,
		"signcheck: signing as %q, so the privacy grant survives this install.\n",
		dibsIdentity)
}

// dibsIdentity is the only signing identity this tool will name.
//
// It used to list every code-signing identity in the keychain and propose the
// first one, which is wrong twice over.
//
// Borrowing another project's identity is not a convenience, it is a
// cross-project dependency established by accident: Dibs' privacy grant would
// then be keyed to a certificate some unrelated project owns, and rotating or
// deleting it there silently revokes the grant here.
//
// And PRINTING the list is a disclosure. A keychain holds identities for work
// that has not been announced, and this tool's output is the kind of thing that
// gets pasted into an issue or captured in a CI log. Dibs has no business
// naming anything but its own.
const dibsIdentity = "Dibs Local Codesign"

// haveIdentity reports whether the named identity is in the keychain.
//
// By name, never by position: `security` orders identities by keychain, and
// "whatever is first" is how the wrong project's certificate got proposed.
func haveIdentity(name string) bool {
	out, err := exec.Command("security", "find-identity", "-v", "-p", "codesigning").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), `"`+name+`"`)
}

// protectedPaths reports the trees this install would need permission for: the
// data directory and the checkout being installed from.
func protectedPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	candidates := []string{os.Getenv("DIBS_DIR")}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	var out []string
	for _, c := range candidates {
		if c == "" {
			continue
		}
		for _, name := range []string{"Desktop", "Documents", "Downloads"} {
			guard := filepath.Join(home, name)
			if c == guard || strings.HasPrefix(c, guard+string(filepath.Separator)) {
				out = append(out, c)
			}
		}
	}
	return out
}
