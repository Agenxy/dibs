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
	"errors"
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
		// MAKE one, rather than explaining how.
		//
		// The old text sent the operator to Keychain Access, through Certificate
		// Assistant, to create a certificate with four fields set exactly right.
		// Measured on the machine this was written on: they did not do it, and
		// were then asked for Desktop access on every single install for a day,
		// because every ad-hoc rebuild is a different program to macOS. An
		// instruction nobody follows is a defect with extra steps.
		//
		// It is a self-signed code-signing certificate in the user's own login
		// keychain. Nothing is trusted beyond this machine and nothing is sent
		// anywhere: the point is only that the signature stops changing, so the
		// privacy grant they give once keeps applying.
		//
		// The one step that cannot be silent is trust, which macOS gates behind
		// the user's password. That prompt is correct and worth it: one
		// authorisation, once, against a folder prompt on every install forever.
		if err := createIdentity(); err != nil {
			fmt.Fprintf(os.Stderr,
				"signcheck: %s is in a macOS protected folder and dibd will be ad-hoc\n"+
					"  signed, so any Desktop/Documents/Downloads permission you grant is\n"+
					"  revoked by the next install.\n\n"+
					"  Tried to create a signing identity for you and could not: %v\n\n"+
					"  Do it by hand if you would rather: Keychain Access → Certificate\n"+
					"  Assistant → Create a Certificate, named %q, type Code Signing,\n"+
					"  self-signed. Every install finds it by name after that.\n",
				guarded[0], err, dibsIdentity)
			return
		}
		fmt.Fprintf(os.Stderr,
			"signcheck: created a signing identity named %q in your login keychain.\n"+
				"  macOS ties a Files-and-Folders permission to a program's signature, and\n"+
				"  the Go toolchain signs ad-hoc, so without this every install would ask\n"+
				"  you for Desktop access again. It will not now.\n", dibsIdentity)
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

// createIdentity makes Dibs' own self-signed code-signing certificate and
// trusts it for code signing, in the user's login keychain.
//
// Written as exec calls from Go rather than a shell script, per CONTRIBUTING:
// this is a sequence with four failure points and a private key passing through
// a temporary file, which is exactly the kind of thing that should not live in
// untyped glue that continues past errors.
//
// The key never leaves this machine and the certificate is trusted only in the
// user's own keychain. It is not an Apple identity and does not pretend to be:
// its whole job is to be the SAME signature next time, so that a permission the
// operator grants once keeps applying.
func createIdentity() error {
	dir, err := os.MkdirTemp("", "dibs-signing-*")
	if err != nil {
		return err
	}
	// The private key is in here. Remove it whatever happens.
	defer func() { _ = os.RemoveAll(dir) }()

	key := filepath.Join(dir, "key.pem")
	crt := filepath.Join(dir, "cert.pem")
	p12 := filepath.Join(dir, "bundle.p12")
	const pass = "dibs-transient" // #nosec G101 -- guards a file deleted seconds later

	steps := [][]string{
		{
			"openssl", "req", "-x509", "-newkey", "rsa:2048", "-sha256", "-days", "3650",
			"-nodes", "-keyout", key, "-out", crt, "-subj", "/CN=" + dibsIdentity,
			"-addext", "basicConstraints=critical,CA:FALSE",
			"-addext", "keyUsage=critical,digitalSignature",
			"-addext", "extendedKeyUsage=critical,codeSigning",
		},
		// -legacy, because macOS's Security framework rejects the PKCS#12 that
		// OpenSSL 3 writes by default: "MAC verification failed during PKCS12
		// import", which reads like a wrong password and is not one.
		{
			"openssl", "pkcs12", "-export", "-legacy", "-out", p12, "-inkey", key,
			"-in", crt, "-passout", "pass:" + pass, "-name", dibsIdentity,
		},
		{
			"security", "import", p12, "-k", loginKeychain(), "-P", pass,
			"-T", "/usr/bin/codesign", "-A",
		},
		// The step that prompts. Without it the certificate is in the keychain
		// and `security find-identity -p codesigning` will not list it, so
		// codesign cannot use it and the whole exercise achieves nothing.
		{
			"security", "add-trusted-cert", "-r", "trustRoot", "-p", "codeSign",
			"-k", loginKeychain(), crt,
		},
	}
	for _, step := range steps {
		// #nosec G204 -- every argument is a constant or a path this function
		// created; nothing here comes from outside.
		if out, err := exec.Command(step[0], step[1:]...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w: %s", step[0], err, strings.TrimSpace(string(out)))
		}
	}
	if !haveIdentity(dibsIdentity) {
		return errors.New("the certificate was created but is not usable for code " +
			"signing; the trust step may have been declined")
	}
	return nil
}

func loginKeychain() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "login.keychain-db"
	}
	return filepath.Join(home, "Library", "Keychains", "login.keychain-db")
}
