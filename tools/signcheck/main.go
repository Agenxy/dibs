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
	"regexp"
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
	ids := identities()
	if len(ids) == 0 {
		// Nothing to sign with. Say what will happen, but do not block: the fix
		// is not available to this machine yet.
		fmt.Fprintf(os.Stderr,
			"signcheck: %s is in a macOS protected folder and dibd will be ad-hoc signed,\n"+
				"  so any Desktop/Documents/Downloads permission you grant is revoked by the\n"+
				"  next install. Create a signing identity in Keychain Access (a self-signed\n"+
				"  code-signing certificate is enough) and set DIBS_CODESIGN_IDENTITY.\n",
			guarded[0])
		return 0
	}
	fmt.Fprintf(os.Stderr,
		"\nsigncheck: refusing to install ad-hoc, because it would revoke a permission you have to re-grant.\n\n"+
			"  %s is inside a macOS protected folder, so dibd needs your permission to read it.\n"+
			"  macOS ties that permission to the binary's signature, and an ad-hoc signature\n"+
			"  changes on every build: you would be prompted again, exactly as before.\n\n"+
			"  Install with an identity you already have:\n\n"+
			"    DIBS_CODESIGN_IDENTITY=%q task install\n\n"+
			"  Available: %s\n\n"+
			"  Or accept the re-prompt: DIBS_ADHOC_OK=1 task install\n\n",
		guarded[0], ids[0], strings.Join(ids, ", "))
	return 1
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

// identities lists code-signing identities in the keychain, newest first as
// `security` reports them.
func identities() []string {
	out, err := exec.Command("security", "find-identity", "-v", "-p", "codesigning").Output()
	if err != nil {
		return nil
	}
	// Lines look like: `  1) A1B2C3… "Some Local Codesign"`
	re := regexp.MustCompile(`\d+\)\s+[0-9A-F]+\s+"([^"]+)"`)
	var names []string
	for _, m := range re.FindAllStringSubmatch(string(out), -1) {
		names = append(names, m[1])
	}
	return names
}
