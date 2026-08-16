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

func main() { os.Exit(run()) }

func run() int {
	if runtime.GOOS != "darwin" || os.Getenv("DIBS_CODESIGN_IDENTITY") != "" {
		return 0
	}
	if os.Getenv("DIBS_ADHOC_OK") == "1" {
		fmt.Println("signcheck: ad-hoc install allowed by DIBS_ADHOC_OK=1")
		return 0
	}
	guarded := protectedPaths()
	if len(guarded) == 0 {
		return 0 // nothing here needs the grant
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
		return 0
	}
	fmt.Fprintf(os.Stderr,
		"\nsigncheck: refusing to install ad-hoc, because it would revoke a permission you have to re-grant.\n\n"+
			"  %s is inside a macOS protected folder, so dibd needs your permission to read it.\n"+
			"  macOS ties that permission to the binary's signature, and an ad-hoc signature\n"+
			"  changes on every build: you would be prompted again, exactly as before.\n\n"+
			"  Install with the identity Dibs already has:\n\n"+
			"    DIBS_CODESIGN_IDENTITY=%q task install\n\n"+
			"  Or accept the re-prompt: DIBS_ADHOC_OK=1 task install\n\n",
		guarded[0], dibsIdentity)
	return 1
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
