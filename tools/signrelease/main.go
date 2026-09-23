// Command signrelease gives the published macOS binaries an identity.
//
// The gap it closes was found by deploying a hub to a second Mac. The
// published `dibd` introduced itself to macOS as `a.out`, the Go toolchain's
// default, with an ad-hoc signature. Two things follow from that, and both
// land on the operator:
//
//   - macOS keys a firewall allowance, like a privacy grant, to the program's
//     SIGNATURE. An ad-hoc signature gets a fresh code-directory hash from
//     every build, so every update is a different program and the allowance
//     stops applying. `tools/signid` exists because this repository already
//     measured that for privacy grants: nine installs in one session, nine
//     prompts. The firewall behaves the same way, which means a hub asks its
//     operator to be physically present for every upgrade.
//   - `a.out` is not an identity. Every ad-hoc Go binary on the machine
//     shares it, so anything the system records against the name is recorded
//     against all of them.
//
// `task install` has signed with a stable identity and an explicit identifier
// since the privacy-grant fix. The RELEASE did neither, so a source install
// was better behaved than the official one. That is backwards, and this is
// the thing that runs in the release.
//
// What it does NOT do is make Gatekeeper accept the binary. That needs a
// Developer ID certificate and notarization, which needs an Apple Developer
// Program membership. A self-signed identity is the free half: the operator
// still approves once, and is not asked again on every update.
//
// A Go tool rather than shell in the workflow, per CONTRIBUTING, and for the
// reason this file is about: the failure mode is silent. A signing step that
// quietly does nothing produces a release that looks identical and behaves
// differently three weeks later on somebody else's machine.
package main

import (
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The identifiers each shipped artifact presents to macOS. They are stable
// across versions on purpose: an allowance is recorded against the identifier
// plus the certificate, and a name that moved with the version would ask the
// operator again every release, which is the whole thing this avoids.
var identifiers = map[string]string{
	"dibd":          "org.agenxy.dibs",
	"dibs":          "org.agenxy.dibs.cli",
	"dibs-presence": "org.agenxy.dibs.presence",
	"Dibs.app":      "org.agenxy.dibs.notify",
}

const (
	// p12Env carries the signing identity, base64 of a PKCS#12 bundle. Absent,
	// everything below still runs and signs ad-hoc WITH the right identifier,
	// which is the half that needs no secret.
	p12Env      = "DIBS_SIGNING_P12"
	passwordEnv = "DIBS_SIGNING_P12_PASSWORD" // #nosec G101 -- the NAME of a variable
	// keychainEnv is how -sign finds what -import made, across the separate
	// processes GoReleaser invokes.
	keychainEnv = "DIBS_SIGNING_KEYCHAIN"
	identityEnv = "DIBS_CODESIGN_IDENTITY"
)

func main() {
	imp := flag.Bool("import", false, "make a keychain from "+p12Env+" and print what to export")
	sign := flag.String("sign", "", "sign this binary or app bundle")
	flag.Parse()

	var err error
	switch {
	case *imp:
		err = importIdentity()
	case *sign != "":
		err = signOne(*sign)
	default:
		fmt.Fprintln(os.Stderr, "usage: signrelease -import | -sign <path>")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "signrelease:", err)
		os.Exit(1)
	}
}

// importIdentity puts the signing certificate somewhere codesign can find it,
// and says plainly when there is none.
//
// Saying so matters more than it looks. A release signed ad-hoc is a working
// release; what it costs is one prompt per update on every operator's machine,
// months later, with nothing connecting the two. The line printed here is the
// only moment that is visible.
func importIdentity() error {
	blob := strings.TrimSpace(os.Getenv(p12Env))
	if blob == "" {
		fmt.Println("no " + p12Env + " in the environment: the release will be signed " +
			"ad-hoc, with correct identifiers and a code-directory hash that changes " +
			"every build. Operators will be asked to allow it through the firewall " +
			"again on every update.")
		return nil
	}
	der, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return fmt.Errorf("%s is not base64: %w", p12Env, err)
	}
	dir, err := os.MkdirTemp("", "dibs-signing-")
	if err != nil {
		return err
	}
	p12 := filepath.Join(dir, "identity.p12")
	// #nosec G703 -- p12 is MkdirTemp's own directory plus a fixed name; no
	// caller-supplied path reaches it.
	if err := os.WriteFile(p12, der, 0o600); err != nil {
		return err
	}
	keychain := filepath.Join(dir, "signing.keychain-db")
	// A password for the KEYCHAIN, not for the identity. It exists for the
	// length of one job on a throwaway runner and never leaves it; what it
	// protects against is a keychain that unlocks itself, which `security`
	// refuses to import into.
	pass := "dibs-" + filepath.Base(dir)
	steps := [][]string{
		{"create-keychain", "-p", pass, keychain},
		{"set-keychain-settings", "-lut", "3600", keychain},
		{"unlock-keychain", "-p", pass, keychain},
		{
			"import", p12, "-k", keychain, "-P", os.Getenv(passwordEnv),
			"-T", "/usr/bin/codesign", "-f", "pkcs12",
		},
		// Without this, codesign blocks on a GUI prompt that a runner cannot
		// answer, and the job hangs until it times out rather than failing.
		{"set-key-partition-list", "-S", "apple-tool:,apple:,codesign:", "-s", "-k", pass, keychain},
	}
	for _, args := range steps {
		if out, err := security(args...); err != nil {
			return fmt.Errorf("security %s: %w\n%s", args[0], err, out)
		}
	}
	// The keychain has to be on the SEARCH LIST or codesign cannot see it,
	// and the list is replaced rather than appended to, so the login keychain
	// has to be named again or every other lookup on the runner breaks.
	existing, err := security("list-keychains", "-d", "user")
	if err != nil {
		return err
	}
	list := append([]string{"list-keychains", "-d", "user", "-s", keychain}, quoted(existing)...)
	if out, err := security(list...); err != nil {
		return fmt.Errorf("security list-keychains: %w\n%s", err, out)
	}
	sha, err := certificateIn(keychain)
	if err != nil {
		return err
	}
	return publish(map[string]string{keychainEnv: keychain, identityEnv: sha})
}

// publish hands the two values to whatever runs the next step.
//
// It writes GITHUB_ENV itself rather than leaving the caller to redirect,
// which is not tidiness: `run: ... >> "$GITHUB_ENV"` is a shell redirection,
// and TestWorkflowsDoNotEmbedShellScripts refuses those, for the reason
// CONTRIBUTING gives. The contract belongs to the tool anyway. Outside a
// workflow it prints, so the same command is useful by hand.
func publish(vars map[string]string) error {
	var b strings.Builder
	for _, k := range []string{keychainEnv, identityEnv} {
		fmt.Fprintf(&b, "%s=%s\n", k, vars[k])
	}
	path := strings.TrimSpace(os.Getenv("GITHUB_ENV"))
	if path == "" {
		fmt.Print(b.String())
		return nil
	}
	// #nosec G304,G703 -- GITHUB_ENV is set by the runner for exactly this, and
	// this process is the one the runner started.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return err
	}
	fmt.Println("signing identity exported to the job environment")
	return f.Close()
}

// certificateIn reports the SHA-1 of the one signing certificate in a
// keychain, and refuses to guess between several: a release signed by
// whichever certificate came first is a release nobody can predict the
// identity of.
//
// By HASH, not by name through `security find-identity`, and the difference
// is not cosmetic. find-identity reports "0 valid identities found" for a
// self-signed certificate, with or without a policy, because nothing trusts
// it: the obvious call returns nothing and says nothing is wrong. codesign
// accepts a certificate hash directly and signs perfectly well with it, which
// is what makes this work without asking the machine to trust a new root.
// Measured both ways before choosing.
func certificateIn(keychain string) (string, error) {
	out, err := security("find-certificate", "-a", "-Z", keychain)
	if err != nil {
		return "", fmt.Errorf("security find-certificate: %w\n%s", err, out)
	}
	var hashes []string
	for _, line := range strings.Split(out, "\n") {
		if rest, found := strings.CutPrefix(strings.TrimSpace(line), "SHA-1 hash:"); found {
			hashes = append(hashes, strings.TrimSpace(rest))
		}
	}
	switch len(hashes) {
	case 0:
		return "", errors.New("the imported keychain holds no certificate")
	case 1:
		return hashes[0], nil
	default:
		return "", fmt.Errorf("the imported keychain holds %d certificates; a release has "+
			"to be signed by one, and which is not this tool's guess to make", len(hashes))
	}
}

// signOne signs a built artifact, with the certificate if there is one and
// ad-hoc if there is not, and ALWAYS with the identifier that artifact should
// present to macOS.
func signOne(path string) error {
	name := filepath.Base(path)
	id, known := identifiers[name]
	if !known {
		return fmt.Errorf("%s is not something this release ships; the identifiers are %v",
			name, keys(identifiers))
	}
	if !isMachO(path) {
		// The Linux builds go through the same hook. Nothing to sign, and
		// saying so beats a hook that appears to have done something.
		fmt.Printf("%s is not a macOS binary; not signing\n", path)
		return nil
	}
	identity := strings.TrimSpace(os.Getenv(identityEnv))
	if identity == "" {
		identity = "-"
	}
	args := []string{"--force", "--identifier", id, "--sign", identity}
	if keychain := strings.TrimSpace(os.Getenv(keychainEnv)); keychain != "" {
		args = append(args, "--keychain", keychain)
	}
	// NO TIMESTAMP. A trusted timestamp is a network call to Apple whose
	// answer differs every run, and the README claims these binaries are
	// reproducible. It buys nothing here either: a timestamp proves a
	// signature predates a certificate's expiry, and a self-signed identity
	// has no revocation story for that to matter to.
	args = append(args, "--timestamp=none", path)
	// #nosec G204,G702 -- no shell; every argument is built in this function,
	// and path comes from GoReleaser naming a file it has just built.
	if out, err := exec.Command("codesign", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("codesign %s: %w\n%s", path, err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("signed %s as %s (%s)\n", path, id, identity)
	return nil
}

// isMachO reports whether a path is something codesign can sign: a Mach-O
// binary, or a bundle directory.
func isMachO(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	if st.IsDir() {
		return strings.HasSuffix(path, ".app")
	}
	f, err := os.Open(path) // #nosec G304 -- a file GoReleaser just built
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	var magic [4]byte
	if _, err := f.Read(magic[:]); err != nil {
		return false
	}
	switch magic {
	case [4]byte{0xcf, 0xfa, 0xed, 0xfe}, // 64-bit little endian
		[4]byte{0xce, 0xfa, 0xed, 0xfe}, // 32-bit little endian
		[4]byte{0xca, 0xfe, 0xba, 0xbe}: // universal
		return true
	}
	return false
}

func security(args ...string) (string, error) {
	// #nosec G204,G702 -- no shell; arguments are built in this file.
	out, err := exec.Command("security", args...).CombinedOutput()
	return string(out), err
}

// quoted pulls the keychain paths out of `security list-keychains` output,
// which prints each one wrapped in quotes and indented.
func quoted(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if len(line) > 2 && strings.HasPrefix(line, `"`) && strings.HasSuffix(line, `"`) {
			paths = append(paths, line[1:len(line)-1])
		}
	}
	return paths
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
