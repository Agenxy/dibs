package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/remap"
	"github.com/agenxy/dibs/internal/supgang"
)

// The test binary doubles as a stand-in `supgang` that answers as the real
// one did on 2026-09-13, so the join-by-peer path is exercised against real
// envelopes.
func TestMain(m *testing.M) {
	if os.Getenv("DIBS_TEST_AS_SUPGANG") != "" {
		os.Exit(fakeSupgang(os.Args[1:]))
	}
	// No test here reaches the real supgang or remap on this machine's PATH:
	// a developer's hive would otherwise answer for the fleet a test set up,
	// and the wizard would register a name in their own registry every time
	// the suite ran.
	supgang.Command = "/nonexistent/supgang-under-test"
	remap.Command = "/nonexistent/remap-under-test"
	// And as `dibs` itself, for the pi extension under test, which asks the
	// binary what this machine and checkout are (`dibs identity`).
	if os.Getenv("DIBS_TEST_AS_DIBS") != "" && len(os.Args) > 1 && os.Args[1] == "identity" {
		// Where the hub is now, as a peer resolution would have found it:
		// the test names it directly rather than standing up Supgang.
		if moved := os.Getenv("DIBS_TEST_IDENTITY_ORIGIN"); moved != "" {
			identityOrigin = func() string { return moved }
		}
		if err := identityCmd(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeSupgang(args []string) int {
	if len(args) < 2 || args[0] != "--json" {
		return 2
	}
	switch args[1] {
	case "status":
		_, _ = os.Stdout.WriteString(`{"schema":"supgang.status/v4","status":"ok","version":"0.2.0-alpha.10","name":"MacMarine","hive_id":"5cb4e356","node_id":"fed08b444ee029ef43b8c04106199408f449d6857f60c16423d32f8dbbe77621","service":"running","mode":"device"}`)
	case "resolve":
		if len(args) < 3 || (args[2] != "MacSolis" && args[2] != "solis") {
			_, _ = os.Stdout.WriteString(`{"schema":"supgang.error/v1","status":"error","error":"no known peer significantly matches that name, tag, or fingerprint"}`)
			return 3
		}
		// DIBS_TEST_SUPGANG_DIBS=<host>:<port>:<pin> makes MacSolis a computer
		// that advertises its Dibs (resolve/v5), reachable at <host>.
		host, schema, services := "192.168.1.191", "supgang.resolve/v4", ""
		if adv := os.Getenv("DIBS_TEST_SUPGANG_DIBS"); adv != "" {
			parts := strings.SplitN(adv, ":", 3)
			host, schema = parts[0], "supgang.resolve/v5"
			services = `,"services":[{"name":"dibs","port":` + parts[1] + `,"key_pin":"` + parts[2] + `"}]`
		}
		_, _ = os.Stdout.WriteString(`{"schema":"` + schema + `","status":"ok","node_id":"a8a37e32c37cdf4fb7634de622bc3f84ccb4636580d1ea26af3fe8ac31d1f152","name":"MacSolis","tags":["solis"],"fingerprint":"a8a37e32","candidates":[{"scope":"local","kind":"local","transport":"quic-v1","address":"` + host + `:44330","provenance":"device-signed","route_compatible":true,"preferred":true}]` + services + `}`)
	default:
		return 2
	}
	return 0
}

func useFakeSupgang(t *testing.T) {
	t.Helper()
	old := supgang.Command
	supgang.Command = os.Args[0]
	t.Setenv("DIBS_TEST_AS_SUPGANG", "1")
	t.Cleanup(func() { supgang.Command = old })
}

// `dibs mcp-config --board MacSolis` joins the hub the way the fleet names
// it: Supgang's signed address for that computer, Dibs's own port, https,
// and the peer recorded so the bridge can ask again later. An address is
// still an address, and a name Supgang does not know is not turned into one.
func TestABoardCanBeNamedAsASupgangPeer(t *testing.T) {
	useFakeSupgang(t)
	remote, peer, err := resolveBoardPeer("MacSolis")
	if err != nil || remote != "https://192.168.1.191:4777" || peer == nil || peer.Fingerprint != "a8a37e32" {
		t.Fatalf("resolveBoardPeer(MacSolis) = %q %+v %v", remote, peer, err)
	}
	if remote, _, err := resolveBoardPeer("solis:4790"); err != nil || remote != "https://192.168.1.191:4790" {
		t.Errorf("resolveBoardPeer(solis:4790) = %q %v: the port is Dibs's, not Supgang's", remote, err)
	}
	for _, bad := range []string{"solis:0", "solis:65536", "solis:-1", "solis:x"} {
		if _, _, err := resolveBoardPeer(bad); err == nil || errors.Is(err, errNotAPeer) {
			t.Errorf("resolveBoardPeer(%q) = %v, want a refusal naming the port", bad, err)
		}
	}
	// Two boards on one peer are two secrets: their directories differ.
	t.Setenv("HOME", t.TempDir())
	dirs := map[string]bool{}
	for _, b := range []string{"MacSolis", "MacSolis:4790"} {
		remote, peer, err := resolveBoardPeer(b)
		if err != nil {
			t.Fatal(err)
		}
		dirs[joinDirFor(remote, peer)] = true
	}
	if len(dirs) != 2 {
		t.Errorf("two boards on one peer share a credential directory: %v", dirs)
	}
	for _, addr := range []string{"192.168.1.5:4777", "https://hub:4777", "[::1]:4777", "nobody", "nobody:4777"} {
		if _, _, err := resolveBoardPeer(addr); !errors.Is(err, errNotAPeer) {
			t.Errorf("resolveBoardPeer(%q) = %v, want errNotAPeer so the address path takes over", addr, err)
		}
	}
}

// hubAdvertising is a TLS server standing in for a hub, and the pin of the
// key it serves: what that hub's computer would sign through Supgang.
func hubAdvertising(t *testing.T) (srv *httptest.Server, host, port, pin string) {
	t.Helper()
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	t.Cleanup(srv.Close)
	host, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return srv, host, port, keyPin(srv.Certificate())
}

// A hub that advertised its Dibs through Supgang is joined on the port it
// signed, and its certificate is checked against the key it signed and
// recorded, so the recipe has no fingerprint for a person to compare. A
// certificate with another key is refused outright: nothing is recorded and
// no recipe is printed, because whatever answered is not that board.
func TestAJoinVerifiesTheHubsCertificateAgainstTheKeyItSigned(t *testing.T) {
	useFakeSupgang(t)
	t.Setenv("HOME", t.TempDir())
	srv, host, port, pin := hubAdvertising(t)
	t.Setenv("DIBS_TEST_SUPGANG_DIBS", host+":"+port+":"+pin)

	remote, peer, err := resolveBoardPeer("MacSolis")
	if err != nil || remote != "https://"+net.JoinHostPort(host, port) {
		t.Fatalf("resolveBoardPeer = %q %v, want the advertised port", remote, err)
	}
	if r, _, err := resolveBoardPeer("MacSolis:4800"); err != nil || !strings.HasSuffix(r, ":4800") {
		t.Errorf("an explicit port was overridden by the advertisement: %q %v", r, err)
	}
	out, err := captureStdout(t, func() error { return printJoinConfigFor(remote, peer) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing to compare by hand") || strings.Contains(out, "dibs trust") {
		t.Errorf("the recipe still asks for a ceremony:\n%s", out)
	}
	recorded, err := os.ReadFile(filepath.Join(joinDirFor(remote, peer), "trusted-certs.pem"))
	if err != nil {
		t.Fatalf("nothing recorded in the board's data directory: %v", err)
	}
	block, _ := pem.Decode(recorded)
	if block == nil {
		t.Fatal("the trust store is not PEM")
	}
	if c, err := x509.ParseCertificate(block.Bytes); err != nil || !c.Equal(srv.Certificate()) {
		t.Error("what was recorded is not the certificate the hub serves")
	}

	// An impostor: the same address, a key the hub's computer never signed.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DIBS_TEST_SUPGANG_DIBS", host+":"+port+":"+strings.Repeat("00", 32))
	remote, peer, err = resolveBoardPeer("MacSolis")
	if err != nil {
		t.Fatal(err)
	}
	out, err = captureStdout(t, func() error { return printJoinConfigFor(remote, peer) })
	if err == nil || !strings.Contains(err.Error(), "not that board") {
		t.Fatalf("a certificate with the wrong key was accepted: err=%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(joinDirFor(remote, peer), "trusted-certs.pem")); err == nil {
		t.Error("the impostor's certificate was recorded")
	}
	if strings.Contains(out, "mcpServers") {
		t.Error("a recipe was printed for a board that failed its key check")
	}

	// A hub that is not answering is not an impostor: the pin travels into
	// the trust command, which makes the same comparison later.
	t.Setenv("DIBS_TEST_SUPGANG_DIBS", host+":"+port+":"+pin)
	if remote, peer, err = resolveBoardPeer("MacSolis"); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	out, err = captureStdout(t, func() error { return printJoinConfigFor(remote, peer) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dibs trust "+shellArg(net.JoinHostPort(host, port))+" --pin "+pin) {
		t.Errorf("an unreachable hub's recipe does not carry the signed pin into the trust step:\n%s", out)
	}
}

// issuedChain is a board CA and a leaf it issued for 127.0.0.1, or, with
// impostor set, a self-signed leaf for 127.0.0.1 with the same CA appended:
// the shape an impostor presents so that the TOP of its chain carries the
// key the hub's computer signed while the certificate protecting the
// connection is its own.
func issuedChain(t *testing.T, impostor bool) (chain tls.Certificate, ca *x509.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "board CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, BasicConstraintsValid: true, IsCA: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "leaf"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	parent, signer := caTmpl, caKey
	if impostor {
		parent, signer = leafTmpl, leafKey
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, parent, &leafKey.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}, ca
}

func serveChain(t *testing.T, chain tls.Certificate) (target string) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{chain}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// The pin names the board CA, and the certificate protecting the connection
// must be issued under it. An impostor that appends the hub's public CA to
// its own chain makes the top of the chain match the pin; it is refused,
// because the leaf is not the CA's. The real shape, a leaf the CA issued,
// is accepted and the CA recorded.
func TestAPinnedJoinRequiresTheLeafToBeIssuedUnderThePinnedKey(t *testing.T) {
	genuine, ca := issuedChain(t, false)
	dir := t.TempDir()
	if cert, err := trustPinned(dir, serveChain(t, genuine), keyPin(ca)); err != nil || !cert.Equal(ca) {
		t.Fatalf("a leaf issued under the pinned CA was refused: %v", err)
	}
	forged, ca := issuedChain(t, true)
	dir = t.TempDir()
	_, err := trustPinned(dir, serveChain(t, forged), keyPin(ca))
	if err == nil || !strings.Contains(err.Error(), "not issued under that key") {
		t.Fatalf("an impostor's leaf with the hub's CA appended was accepted: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "trusted-certs.pem")); serr == nil {
		t.Error("and the CA it appended was recorded")
	}
	// The unpinned ceremony records what is presented, as it always did: the
	// human comparison is the check there, and the bridge's own TLS
	// verification refuses the impostor's leaf on the first call.
	if _, err := trustPinned(t.TempDir(), serveChain(t, forged), ""); err != nil {
		t.Errorf("the unpinned ceremony grew a check it never had: %v", err)
	}
}

// `--pin ""` is not "no pin": a shell variable that came out empty must not
// silently turn the verified join into the unverified one.
func TestAnEmptyPinIsRefusedNotIgnored(t *testing.T) {
	_, host, port, _ := hubAdvertising(t)
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	for _, args := range [][]string{
		{net.JoinHostPort(host, port), "--pin", ""},
		{net.JoinHostPort(host, port), "--pin"}, // an unquoted $PIN that came out empty
	} {
		if err := trustCmd(args); err == nil {
			t.Fatalf("%q recorded the certificate unverified, or reported success", args)
		}
		if _, err := os.Stat(filepath.Join(dir, "trusted-certs.pem")); err == nil {
			t.Errorf("%q recorded it", args)
		}
	}
}

// `--board MacSolis:04777` is port 4777 however it is spelt: the pin check
// keys on the port, and a spelling that slipped past the comparison would
// turn the verified join into the unpinned ceremony.
func TestAZeroPaddedPortStillVerifiesThePin(t *testing.T) {
	useFakeSupgang(t)
	t.Setenv("HOME", t.TempDir())
	_, host, port, _ := hubAdvertising(t)
	t.Setenv("DIBS_TEST_SUPGANG_DIBS", host+":"+port+":"+strings.Repeat("00", 32))
	remote, peer, err := resolveBoardPeer("MacSolis:0" + port)
	if err != nil || !strings.HasSuffix(remote, ":"+port) {
		t.Fatalf("resolveBoardPeer = %q %v", remote, err)
	}
	out, err := captureStdout(t, func() error { return printJoinConfigFor(remote, peer) })
	if err == nil || !strings.Contains(err.Error(), "not that board") {
		t.Fatalf("a zero-padded port skipped the pin check: err=%v\n%s", err, out)
	}
}

// A hub whose advertisement is broken (a pin that is not a key) is a fault
// to name, not an absence to fall back from: the join refuses rather than
// printing the unpinned ceremony as if nothing were advertised.
func TestABrokenAdvertisementIsAFaultNotAnAbsence(t *testing.T) {
	useFakeSupgang(t)
	t.Setenv("HOME", t.TempDir())
	_, host, port, _ := hubAdvertising(t)
	t.Setenv("DIBS_TEST_SUPGANG_DIBS", host+":"+port+":notakeypin")
	remote, peer, err := resolveBoardPeer("MacSolis")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(remote, ":4777") {
		t.Errorf("the port of a broken advertisement was believed: %s", remote)
	}
	out, err := captureStdout(t, func() error { return printJoinConfigFor(remote, peer) })
	if err == nil || !strings.Contains(err.Error(), "dibs fingerprint") {
		t.Fatalf("a broken advertisement fell back to the unpinned ceremony: err=%v\n%s", err, out)
	}
	msg, _ := hubAdvertisementDrift(*peer, port, strings.Repeat("ab", 32))
	if !strings.Contains(msg, "not one a joining machine can use") {
		t.Errorf("doctor on that hub said %q", msg)
	}
}

// `dibs trust --pin` is that same check by hand: the certificate is recorded
// only when its key is the one given.
func TestTrustWithAPinRefusesAnotherKey(t *testing.T) {
	_, host, port, pin := hubAdvertising(t)
	target := net.JoinHostPort(host, port)
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	if err := trustCmd([]string{target, "--pin", strings.Repeat("ff", 32)}); err == nil {
		t.Fatal("a certificate whose key is not the pinned one was trusted")
	}
	if _, err := os.Stat(filepath.Join(dir, "trusted-certs.pem")); err == nil {
		t.Error("and recorded")
	}
	if err := trustCmd([]string{target, "--pin", "abc"}); err == nil {
		t.Error("a pin that is not a key pin was accepted")
	}
	if _, err := captureStdout(t, func() error { return trustCmd([]string{target, "--pin", strings.ToUpper(pin)}) }); err != nil {
		t.Fatalf("the right key, in upper case, was refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "trusted-certs.pem")); err != nil {
		t.Error("the matching certificate was not recorded")
	}
}

// The bridge dials where Supgang says the hub is NOW, keeping Dibs's scheme
// and port; without a peer, or when Supgang cannot say, the configured
// address is dialled as given.
func TestTheBridgeDialsTheHubWhereSupgangSaysItIsNow(t *testing.T) {
	useFakeSupgang(t)
	t.Setenv("DIBS_ADDR", "https://10.0.0.9:4790")
	t.Setenv(boardPeerEnv, "")
	if got := boardOrigin(); got != "https://10.0.0.9:4790" {
		t.Errorf("without a peer, boardOrigin = %q", got)
	}
	t.Setenv(boardPeerEnv, "MacSolis")
	if got := boardOrigin(); got != "https://192.168.1.191:4790" {
		t.Errorf("with a peer, boardOrigin = %q, want Supgang's host with Dibs's scheme and port", got)
	}
	t.Setenv(boardPeerEnv, "nobody")
	if got := boardOrigin(); got != "https://10.0.0.9:4790" {
		t.Errorf("with a peer Supgang does not know, boardOrigin = %q, want the configured address", got)
	}
	if !strings.HasPrefix(boardOrigin(), "https://") {
		t.Error("the scheme was lost")
	}
	// AND SO DO THE COMMAND HOOKS, which dialled origin() and kept the saved
	// address after the hub moved. Round eighteen of the pre-release review.
	t.Setenv(boardPeerEnv, "MacSolis")
	mcpEndpointOnce, mcpEndpointValue = sync.Once{}, ""
	t.Cleanup(func() { mcpEndpointOnce, mcpEndpointValue = sync.Once{}, "" })
	if got := mcpEndpoint(); got != "https://192.168.1.191:4790/mcp" {
		t.Errorf("a command hook posts to %q, want the hub where Supgang says it is now", got)
	}
}
