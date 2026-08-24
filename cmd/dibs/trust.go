package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"

	"github.com/agenxy/dibs/internal/paths"
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

// trustedPool is the system roots plus every certificate this machine has been
// told to trust, or nil when there are none to add.
//
// Added to the SYSTEM pool rather than replacing it: a daemon fronted by a real
// certificate must keep working, and a client that dropped the system roots
// would refuse it.
func trustedPool() *x509.CertPool {
	pemBytes, err := os.ReadFile(trustFile()) // #nosec G304 -- the daemon's own data directory
	if err != nil {
		return nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
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

// trustCmd implements `dibs trust <host:port>`.
//
// It PRINTS the fingerprint and writes it in one step rather than prompting,
// because a prompt an operator cannot verify against anything is theatre: the
// check that matters is comparing this against what the serving machine reports
// for itself, which is a separate command on a separate machine. So this says
// what it recorded and how to check it, instead of asking a question whose
// answer nobody has yet.
func trustCmd(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Println("usage: dibs trust <host:port>")
		fmt.Println()
		fmt.Println("  Records the certificate a remote dibd is serving, so this machine will")
		fmt.Println("  accept it. Print the daemon's own fingerprint with `dibs fingerprint`")
		fmt.Println("  ON THAT MACHINE and compare the two before relying on it.")
		return nil
	}
	target := args[0]

	// Deliberately unverified: this connection exists to LOOK at the
	// certificate, which is the thing that cannot be verified yet. Nothing is
	// sent over it, and nothing is trusted as a result of it except the bytes
	// the operator is about to be shown.
	conn, err := tls.Dial("tcp", target, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // the certificate is the subject, not the channel
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", target, err)
	}
	defer func() { _ = conn.Close() }()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return fmt.Errorf("%s presented no certificate", target)
	}
	// THE TOP OF THE CHAIN, not the leaf.
	//
	// The daemon presents a short-lived leaf under a long-lived board CA, and
	// the CA is the identity worth pinning: it carries no addresses, so nothing
	// about that machine invalidates it, and every later leaf verifies through
	// it. Pinning the leaf instead would make this ceremony due again on every
	// renewal and every time the board changed networks, on every joined
	// machine at once, which is exactly what the split was made to end.
	//
	// A single self-signed certificate is still a chain of one, so a daemon
	// that predates the split, or one fronted by a real certificate, records
	// what it presents.
	pinned := certs[len(certs)-1]

	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(trustFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: pinned.Raw}); err != nil {
		return err
	}

	fmt.Printf("trusted %s\n", target)
	fmt.Printf("  fingerprint  SHA256:%s\n", fingerprint(pinned.Raw))
	fmt.Printf("  expires      %s\n", pinned.NotAfter.Format("2006-01-02"))
	fmt.Printf("  recorded in  %s\n\n", trustFile())
	fmt.Println("  Verify it: run `dibs fingerprint` on that machine and compare.")
	fmt.Println("  They must match exactly. If they do not, something is answering")
	fmt.Println("  on that address that is not your daemon.")
	return nil
}

// fingerprintCmd implements `dibs fingerprint`: what THIS daemon serves, so the
// value can be compared against what another machine recorded.
func fingerprintCmd(_ []string) error {
	// THE CERTIFICATE THIS DAEMON SERVES, which is not always the managed one.
	//
	// This always read `<dir>/tls-cert.pem`. A board with `tls_cert` configured
	// serves something else entirely, so the command either said no certificate
	// exists or fingerprinted a stale auto-generated chain: on the one command
	// whose whole purpose is comparing what is served against what another
	// machine pinned, and whose mismatch message says something other than your
	// daemon is answering.
	certFile := filepath.Join(paths.DataDir(), "tls-cert.pem")
	if c, cerr := boardconfig.Load(paths.DataDir()); cerr == nil && c.TLSCert != "" {
		certFile = c.TLSCert
	}
	pemBytes, err := os.ReadFile(certFile) // #nosec G304 -- the daemon's own data directory
	if err != nil {
		return fmt.Errorf("no certificate at %s: this daemon serves plaintext on "+
			"loopback and only generates one for an address other machines can reach", certFile)
	}
	// THE LAST BLOCK, because that is what the other machine pinned.
	//
	// This file is a chain now: the short-lived leaf, then the board CA that
	// signed it. `dibs trust` records the top, so printing the first block here
	// would give the operator two different values to compare and tell them
	// something was answering that was not their daemon. The two commands read
	// the same certificate or the ceremony is worse than none.
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
			return err
		}
		cert = c
	}
	if cert == nil {
		return fmt.Errorf("%s is not a PEM certificate", certFile)
	}
	fmt.Printf("SHA256:%s\n", fingerprint(cert.Raw))
	fmt.Printf("expires %s\n", cert.NotAfter.Format("2006-01-02"))
	return nil
}
