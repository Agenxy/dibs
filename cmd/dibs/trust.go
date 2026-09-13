package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/supgang"
)

// Trusting a daemon on another machine, without a certificate authority.
//
// The daemon signs its own certificate off loopback, deliberately: it stands up
// no CA and depends on no VPN (see resolveTransport). That leaves the client
// with a certificate nothing vouches for, which is exactly the position ssh is
// in on first connection, and it has the same answer: look at the fingerprint
// once, record it, and refuse anything that does not match afterwards.
//
// This costs the operator nothing extra. A second machine already needs the
// coordination secret carried across by hand; the fingerprint travels on the
// same trip. What it buys is that "trusted" means THIS daemon, rather than
// whatever answers on that address later.

// trustFile is where accepted certificates are kept: PEM blocks, appended, in
// the daemon's own data directory beside the secret they pair with.
func trustFile() string { return filepath.Join(paths.DataDir(), "trusted-certs.pem") }

// fingerprint is the SHA-256 of the certificate's DER bytes, the same value
// every other tool calls a certificate fingerprint, so an operator can compare
// what Dibs prints against `openssl x509 -fingerprint -sha256`.
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := hex.EncodeToString(sum[:])
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(strings.ToUpper(h[i : i+2]))
	}
	return b.String()
}

// keyPin is the SHA-256 of a certificate's SubjectPublicKeyInfo as 64 lowercase
// hex digits: the RFC 7469 form, and what Dibs advertises through Supgang
// (`supgang advertise dibs <port> --key-pin <this>`). A pin of the KEY rather
// than the certificate, because the board CA is what `dibs trust` records and
// its key is what survives a re-issue.
func keyPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// advertiseCommand is the Supgang verb that publishes this board's key, for
// the hub's operator to run. Supgang signs the profile into its record when
// the service starts, so the verb is wrapped in a stop and a start, the way
// `supgang name set` is.
func advertiseCommand(port, pin string) string {
	return "supgang service stop && supgang advertise " + supgang.ServiceName + " " + port +
		" --key-pin " + pin + " && supgang service start"
}

// ownCAFile is the signing certificate the daemon in this data directory made
// for itself (cmd/dibd ensureSelfSignedCert), when this machine runs one.
func ownCAFile() string { return filepath.Join(paths.DataDir(), "tls-ca.pem") }

// trustedPool is the system roots plus every certificate this machine has been
// told to trust, plus the CA its own daemon signs with, or nil when there is
// nothing to add.
//
// Added to the SYSTEM pool rather than replacing it: a daemon fronted by a real
// certificate must keep working, and a client that dropped the system roots
// would refuse it.
//
// The daemon's own CA is trusted without a `dibs trust` step because the
// machine that generated it is the one authority there is on it. A hub bound
// to a LAN address serves TLS to everybody, its own agents included, and the
// bridge on that machine refused the certificate it lives beside: every local
// call failed with "the daemon was reached and the answer was not" until the
// operator pinned their own daemon by hand, which the join recipe never
// mentions because it is not a join. Found by the two-host e2e suite.
func trustedPool() *x509.CertPool {
	pinned, pinErr := os.ReadFile(trustFile()) // #nosec G304 -- the daemon's own data directory
	own, ownErr := os.ReadFile(ownCAFile())    // #nosec G304 -- the daemon's own data directory
	if pinErr != nil && ownErr != nil {
		return nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	added := pinErr == nil && pool.AppendCertsFromPEM(pinned)
	if ownErr == nil && pool.AppendCertsFromPEM(own) {
		added = true
	}
	if !added {
		return nil
	}
	return pool
}

// daemonClient is the http.Client every command should use to reach dibd.
func daemonClient(timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	rt := http.DefaultTransport
	if pool := trustedPool(); pool != nil {
		rt = &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		}}
	}
	// HERE, because here is the one place every credential-bearing request
	// passes through.
	//
	// The address check and the broken-config check were wired into the callers
	// that had been noticed: mcp-config, the generic get() helper, mcp-stdio.
	// Fifteen call sites build requests with this client, and await, watch,
	// monitor, the admin routes, the hook paths and several doctor probes went
	// straight past both while attaching X-Dibs-Local, and sometimes the admin
	// password. Safety that depends on every future caller remembering is not
	// safety; it is a list of the callers somebody thought of.
	c.Transport = &guardedTransport{next: rt}
	return c
}

// guardedTransport refuses a request that would send this board's credentials
// somewhere they do not belong.
//
// A RoundTripper rather than a helper, so it cannot be skipped by building the
// request differently: whatever assembles the URL, this is what dials it.
type guardedTransport struct{ next http.RoundTripper }

func (g *guardedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// A dibs.toml the daemon will not start on means the board this was meant
	// for is not running, so the endpoint reached now is something else.
	if err := checkConfigReadable(); err != nil {
		return nil, err
	}
	// AND THE AUTHORITY MUST BE THE HOST IT LOOKS LIKE. Everything before an
	// `@` is userinfo: `DIBS_ADDR=http://trusted.example@evil.example:4777`
	// reads as trusted.example everywhere it is printed and dials
	// evil.example. checkAddr catches it, and checkAddr was reachable only
	// through mcp-config.
	if r.URL.User != nil {
		return nil, fmt.Errorf("refusing to send this board's credentials to %q: the "+
			"address names %q before an `@`, which is a username rather than a host, "+
			"so the request would go to %q instead. Name the host on its own",
			r.URL.Host, r.URL.User.Username(), r.URL.Hostname())
	}
	return g.next.RoundTrip(r)
}

// trustCmd implements `dibs trust <host:port> [--pin <hex>]`.
//
// It PRINTS the fingerprint and writes it in one step rather than prompting,
// because a prompt an operator cannot verify against anything is theatre: the
// check that matters is comparing this against what the serving machine reports
// for itself, which is a separate command on a separate machine. So this says
// what it recorded and how to check it, instead of asking a question whose
// answer nobody has yet.
//
// With --pin the comparison is made here: the pin is the key the hub signed
// through Supgang (`dibs mcp-config --board <peer>` carries it into this
// command when the hub could not be reached at the time), and a certificate
// whose key is not that one is refused rather than recorded.
func trustCmd(args []string) error {
	var target, pin string
	pinGiven := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--pin":
			// `--pin` with nothing after it is the same empty pin as
			// `--pin ""`: a check with nothing to check against, refused
			// below, and never a usage message that exits clean.
			pinGiven = true
			if i+1 < len(args) {
				i++
				pin = strings.ToLower(args[i])
			}
		case strings.HasPrefix(args[i], "-") || target != "":
			target = ""
			i = len(args)
		default:
			target = args[i]
		}
	}
	if target == "" {
		fmt.Println("usage: dibs trust <host:port> [--pin <64 hex digits>]")
		fmt.Println()
		fmt.Println("  Records the certificate a remote dibd is serving, so this machine will")
		fmt.Println("  accept it. Print the daemon's own fingerprint with `dibs fingerprint`")
		fmt.Println("  ON THAT MACHINE and compare the two before relying on it; with --pin,")
		fmt.Println("  the key pin that machine advertised through Supgang, the comparison is")
		fmt.Println("  made here and a certificate with another key is refused.")
		return nil
	}
	// `--pin "$PIN"` with the variable unset is not a request to skip the
	// check; it is the check with nothing to check against, and it fails.
	if pinGiven {
		if err := checkPinShape(pin); err != nil {
			return err
		}
	}
	pinned, err := trustPinned(paths.DataDir(), target, pin)
	if err != nil {
		return err
	}
	fmt.Printf("trusted %s\n", target)
	fmt.Printf("  fingerprint  SHA256:%s\n", fingerprint(pinned.Raw))
	fmt.Printf("  key pin      %s\n", keyPin(pinned))
	fmt.Printf("  expires      %s\n", pinned.NotAfter.Format("2006-01-02"))
	fmt.Printf("  recorded in  %s\n\n", trustFile())
	if pin != "" {
		fmt.Println("  Its key is the one that machine signed through Supgang: nothing to compare.")
		return nil
	}
	fmt.Println("  Verify it: run `dibs fingerprint` on that machine and compare.")
	fmt.Println("  They must match exactly. If they do not, something is answering")
	fmt.Println("  on that address that is not your daemon.")
	return nil
}

// chainsToPinned verifies the leaf of certs against its own last certificate
// as the only root, for the host in target.
func chainsToPinned(certs []*x509.Certificate, target string) error {
	roots := x509.NewCertPool()
	roots.AddCert(certs[len(certs)-1])
	inter := x509.NewCertPool()
	if len(certs) > 2 {
		for _, c := range certs[1 : len(certs)-1] {
			inter.AddCert(c)
		}
	}
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}
	_, err = certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, DNSName: host})
	return err
}

// checkPinShape refuses a --pin that could never match anything.
func checkPinShape(pin string) error {
	if len(pin) != 64 || strings.Trim(pin, "0123456789abcdef") != "" {
		return fmt.Errorf("--pin %q is not a key pin: 64 hex digits, the SHA-256 of a public key", pin)
	}
	return nil
}

// servedChain is the chain target presents, leaf first, within a deadline.
//
// Deliberately unverified: this connection exists to LOOK at the certificate,
// which is the thing that cannot be verified yet. Nothing is sent over it, and
// nothing is trusted as a result of it except the bytes the caller then
// compares or shows. Bounded, because `dibs mcp-config --board` dials on the
// operator's behalf and a peer that accepts the connection and never finishes
// the handshake would otherwise hold the recipe forever.
func servedChain(target string) ([]*x509.Certificate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // the certificate is the subject, not the channel
			MinVersion:         tls.VersionTLS12,
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", target, err)
	}
	defer func() { _ = conn.Close() }()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, fmt.Errorf("%s presented no certificate", target)
	}
	return certs, nil
}

// servedCA is the certificate at the top of the chain target presents.
//
// THE TOP OF THE CHAIN, not the leaf. The daemon presents a short-lived leaf
// under a long-lived board CA, and the CA is the identity worth pinning: it
// carries no addresses, so nothing about that machine invalidates it, and
// every later leaf verifies through it. Pinning the leaf instead would make
// this ceremony due again on every renewal and every time the board changed
// networks, on every joined machine at once, which is exactly what the split
// was made to end. A single self-signed certificate is still a chain of one,
// so a daemon that predates the split, or one fronted by a real certificate,
// records what it presents.
func servedCA(target string) (*x509.Certificate, error) {
	certs, err := servedChain(target)
	if err != nil {
		return nil, err
	}
	return certs[len(certs)-1], nil
}

// trustPinned records, in dir, the certificate target serves, after checking
// its key against pin when one is given. A mismatch records nothing: the
// certificate offered is not the one the hub signed through Supgang, and
// whatever is answering on that address is not that board.
func trustPinned(dir, target, pin string) (*x509.Certificate, error) {
	certs, err := servedChain(target)
	if err != nil {
		return nil, err
	}
	cert := certs[len(certs)-1]
	if pin != "" {
		if keyPin(cert) != pin {
			return nil, fmt.Errorf("refusing to trust %s: it presents a certificate whose key pin is %s, "+
				"and the board's computer signed %s through Supgang. Something is answering on "+
				"that address that is not that board, or its Dibs was re-keyed and its "+
				"advertisement not updated (`dibs fingerprint` there prints the current one)",
				target, keyPin(cert), pin)
		}
		// THE LEAF MUST CHAIN TO THE PINNED KEY, and name this address.
		//
		// An impostor can append the hub's PUBLIC CA certificate to its own
		// chain: the top then carries the signed key and the pin matches,
		// while the certificate actually protecting the connection is the
		// impostor's. The bridge would refuse it on the first call, but this
		// command would have reported the join verified. Verified means the
		// leaf is issued under the pinned key, for the host being dialled.
		if err := chainsToPinned(certs, target); err != nil {
			return nil, fmt.Errorf("refusing to trust %s: its certificate carries the key the board's "+
				"computer signed through Supgang, and the certificate it actually serves is not "+
				"issued under that key: %w. Something is answering on that address that is not "+
				"that board", target, err)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// The board's own data directory.
	store := filepath.Join(dir, "trusted-certs.pem")
	f, err := os.OpenFile(store, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
		return nil, err
	}
	return cert, nil
}

// fingerprintCmd implements `dibs fingerprint`: what THIS daemon serves, so the
// value can be compared against what another machine recorded.
// servedCertPath is the certificate THIS daemon presents, which is not always
// the managed one.
//
// Split from the command so a test can ask it. `dibs fingerprint` always read
// `<dir>/tls-cert.pem`, and a board with `tls_cert` configured serves something
// else: the command either said no certificate exists or fingerprinted a stale
// auto-generated chain, on the one command whose whole purpose is comparing
// what is served against what another machine pinned, and whose mismatch
// message says something other than your daemon is answering.
//
// A BROKEN CONFIG IS NOT A MISSING ONE, and this discarded the difference.
//
// boardconfig.Load already separates them: an absent dibs.toml returns no error
// and an unparseable one returns a real error. Swallowing both meant a
// malformed config fell back to the managed path and fingerprinted whatever
// stale certificate was lying there, while `dibd` refuses to start on that same
// file. So the one command whose purpose is comparing what is SERVED against
// what another machine pinned reported a certificate no daemon could serve, and
// exited zero. Found by the pre-release review.
func servedCertPath(dir string) (string, error) {
	c, err := boardconfig.Load(dir)
	if err != nil {
		return "", fmt.Errorf("this board's configuration cannot be read, so which "+
			"certificate it serves is unknown: %w\n"+
			"  A daemon will not start on this file either, so any fingerprint "+
			"printed here would describe a certificate nothing is serving. Fix "+
			"dibs.toml, then ask again", err)
	}
	if c.TLSCert != "" {
		return c.TLSCert, nil
	}
	return filepath.Join(dir, "tls-cert.pem"), nil
}

func fingerprintCmd(_ []string) error {
	// THE CERTIFICATE THIS DAEMON SERVES, which is not always the managed one.
	//
	// This always read `<dir>/tls-cert.pem`. A board with `tls_cert` configured
	// serves something else entirely, so the command either said no certificate
	// exists or fingerprinted a stale auto-generated chain: on the one command
	// whose whole purpose is comparing what is served against what another
	// machine pinned, and whose mismatch message says something other than your
	// daemon is answering.
	certFile, err := servedCertPath(paths.DataDir())
	if err != nil {
		return err
	}
	pemBytes, err := os.ReadFile(certFile) // #nosec G304 -- the daemon's own data directory
	if err != nil {
		return fmt.Errorf("no certificate at %s: this daemon serves plaintext on "+
			"loopback and only generates one for an address other machines can reach", certFile)
	}
	cert, err := lastCertificate(pemBytes)
	if err != nil {
		return fmt.Errorf("%s: %w", certFile, err)
	}
	fmt.Printf("SHA256:%s\n", fingerprint(cert.Raw))
	fmt.Printf("key pin %s\n", keyPin(cert))
	fmt.Printf("expires %s\n", cert.NotAfter.Format("2006-01-02"))
	// The advertisement, so a hub's operator has the exact verb: joining
	// machines then read the pin from Supgang and compare it themselves.
	if p := port(addr()); p != "" {
		fmt.Printf("\nTo let joining machines verify this key instead of comparing it by hand,\n"+
			"advertise it through Supgang on this machine:\n  %s\n", advertiseCommand(p, keyPin(cert)))
	}
	return nil
}

// lastCertificate is the LAST certificate block in a PEM chain, because that
// is what the other machine pinned.
//
// The served file is a chain: the short-lived leaf, then the board CA that
// signed it. `dibs trust` records the top, so reading the first block here
// would give the operator two different values to compare and tell them
// something was answering that was not their daemon. The two commands read
// the same certificate or the ceremony is worse than none.
func lastCertificate(pemBytes []byte) (*x509.Certificate, error) {
	var cert *x509.Certificate
	for rest := pemBytes; ; {
		block, more := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = more
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		cert = c
	}
	if cert == nil {
		return nil, errors.New("not a PEM certificate")
	}
	return cert, nil
}

// servedKeyPin is the pin of the CA the board in dir serves, or an error when
// it serves none (a loopback board) or its configuration cannot be read.
func servedKeyPin(dir string) (string, error) {
	certFile, err := servedCertPath(dir)
	if err != nil {
		return "", err
	}
	pemBytes, err := os.ReadFile(certFile) // #nosec G304 -- the daemon's own data directory
	if err != nil {
		return "", err
	}
	cert, err := lastCertificate(pemBytes)
	if err != nil {
		return "", err
	}
	return keyPin(cert), nil
}
