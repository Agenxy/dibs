package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	xport "github.com/agenxy/dibs/internal/transport"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/liveness"
)

// policy validates the setting and returns it.
func wakePolicy(w WakeConfig) (engine.WakePhase, error) {
	switch w.ExtendTurnFor {
	case "":
		return engine.WakeAll, nil
	case string(engine.WakeUrgent), string(engine.WakeAll), string(engine.WakeNone):
		return engine.WakePhase(w.ExtendTurnFor), nil
	default:
		return "", fmt.Errorf("[wake] extend_turn_for = %q: use \"all\" (default: anything "+
			"unread wakes the agent once), \"urgent\" (only work somebody is blocked on) "+
			"or \"none\" (never extend a turn)", w.ExtendTurnFor)
	}
}

// apply folds the [limits] table over the defaults.
//
// A bad value is an ERROR, never a silent fallback: an operator who wrote
// agent_ttl = "10" (meaning minutes, in a field that takes a duration) and got
// the 5-minute default back would be debugging phantom crashes with no idea the
// setting had been ignored.
func applyLimits(c LimitsConfig, base core.Limits) (core.Limits, error) {
	if err := applyTTL("agent_ttl", c.AgentTTL, &base.AgentTTL); err != nil {
		return base, err
	}
	if err := applyTTL("idle_ttl", c.IdleTTL, &base.IdleTTL); err != nil {
		return base, err
	}
	if err := applyCount("max_persistent_agents", c.MaxPersistentAgents,
		&base.MaxPersistentAgents); err != nil {
		return base, err
	}
	if err := applyCount("max_agents", c.MaxAgents, &base.MaxAgents); err != nil {
		return base, err
	}
	// A persistent ceiling above the total is not a smaller mistake than a bad
	// duration: it reads as configured and the lower bound silently wins, so an
	// operator who raised the wrong one waits for a change that cannot happen.
	if base.MaxPersistentAgents > base.MaxAgents {
		return base, fmt.Errorf(
			"[limits] max_persistent_agents = %d exceeds max_agents = %d, so the "+
				"lower ceiling is the one that binds and this setting would do nothing: "+
				"raise max_agents too, or lower this",
			base.MaxPersistentAgents, base.MaxAgents)
	}
	return base, applyBlobCap(c, &base)
}

// applyCount folds a positive integer limit over the default.
//
// Zero means unset, because a zero ceiling would refuse every registration and
// is never what somebody meant to write. A negative is refused rather than
// clamped: silently correcting a value the operator typed is how a setting ends
// up meaning something other than it says.
func applyCount(key string, raw int, dst *int) error {
	if err := boardconfig.CheckCount("limits", key, raw); err != nil {
		return err
	}
	if raw == 0 {
		return nil
	}
	*dst = raw
	return nil
}

// applyTTL parses one duration limit, refusing anything unusable.
//
// A bad value is an ERROR, never a silent fallback, and the floor is enforced
// for the same reason: agents renew at half the TTL, so anything shorter marks
// healthy agents crashed between their own keepalives: a fleet-wide phantom
// outage produced by a setting that looked accepted.
func applyTTL(key, raw string, dst *time.Duration) error {
	// The check is boardconfig's, so the CLI describing this daemon reaches the
	// same verdict about the same file. Only the assignment is the daemon's.
	d, err := boardconfig.CheckDuration("limits", key, raw)
	if err != nil || raw == "" {
		return err
	}
	*dst = d
	return nil
}

// applyBlobCap folds blob_store_bytes over the default, refusing anything that
// could not hold a single maximum-sized blob: a store smaller than one item
// evicts everything the moment it is used, which looks like attachments simply
// not working.
func applyBlobCap(c LimitsConfig, base *core.Limits) error {
	if c.BlobStoreBytes == 0 {
		return nil
	}
	if min := int64(base.MaxBlobSize); c.BlobStoreBytes < min {
		return fmt.Errorf(
			"[limits] blob_store_bytes = %d is smaller than one maximum blob (%d). "+
				"a store that cannot hold a single attachment evicts everything as soon as "+
				"it is used, which looks like attachments being broken rather than a cap",
			c.BlobStoreBytes, min,
		)
	}
	base.BlobStoreBytes = int(c.BlobStoreBytes)
	return nil
}

// minAgentTTL is the floor below which crash detection stops meaning anything.
// The floor lives with the setting it bounds.
const minAgentTTL = boardconfig.MinAgentTTL

// The configuration types and their loader live in internal/boardconfig,
// because `dibs mcp-config` has to read the same file and reach the same
// verdict. Aliases rather than a rename: every reference in this daemon keeps
// working, and the type is genuinely the same one.
type (
	Config       = boardconfig.Config
	WakeConfig   = boardconfig.WakeConfig
	LimitsConfig = boardconfig.LimitsConfig
	MatchConfig  = boardconfig.MatchConfig
	RolesConfig  = boardconfig.RolesConfig
)

// loadConfig reads <dir>/dibs.toml, refusing anything the operator wrote that
// this daemon would silently ignore.
func loadConfig(dir string) (Config, error) { return boardconfig.Load(dir) }

// transport is the resolved decision about how to serve.
type transport struct {
	certFile, keyFile string // empty ⇒ plaintext
	why               string // one line, logged so the choice is never a mystery
}

// resolveTransport picks the secure option for the address WITHOUT asking the
// user. Good choice architecture: the correct configuration is the one you get
// by doing nothing.
//
//	loopback      → plaintext (nothing else can reach it)
//	anything else → TLS, with a certificate generated on first run
//
// Dibs secures itself: it never asks the operator to stand up a VPN, a proxy,
// or a certificate authority to be safe by default.
func resolveTransport(dir, addr, scheme string, c Config) (transport, error) {
	// The rule itself lives in internal/transport, because `dibs mcp-config`
	// has to reach the same answer in order to print a client configuration
	// that works, and it reached a different one three times. Only the
	// certificate GENERATION is the daemon's, and it stays here.
	choice, err := xport.Resolve(c.TLSCert, c.TLSKey, addr, scheme, c.InsecurePlaintext,
		func() (string, string, error) {
			cert, key, cerr := ensureSelfSignedCert(dir, addr)
			if cerr != nil {
				return "", "", fmt.Errorf("could not prepare TLS for %s: %w", addr, cerr)
			}
			return cert, key, nil
		})
	if err != nil {
		return transport{}, err
	}
	return transport{choice.CertFile, choice.KeyFile, choice.Why}, nil
}

// ensureSelfSignedCert returns a cert/key for addr, generating them into the
// data dir on first use so the operator never has to think about certificates.
//
// TWO CERTIFICATES, and the split is the whole point.
//
// `dibs trust` pins what a daemon presents, ssh-style: look once, record, and
// refuse anything else afterwards. A single self-signed certificate makes the
// pinned identity and the served certificate the same object, and then the two
// things this function must do become mutually exclusive. A bounded lifetime
// needs the certificate replaced; replacing it changes the pinned identity and
// every joined machine refuses the daemon until a human repeats the fingerprint
// ceremony on each one. Not replacing it means an always-on hub sails past
// NotAfter and serves an expired certificate to everybody. The pre-release
// review put both branches side by side, which is what showed there was no
// third one.
//
// So the identity is a long-lived CA that signs a short-lived leaf. The CA
// carries no addresses and never needs reissuing when the machine moves; the
// leaf carries the SANs and is replaced whenever it expires or stops naming the
// address clients dial. `dibs trust` pins the CA, so rotation is invisible to
// every machine that has already trusted this board.
//
// Upgrading from a single self-signed certificate re-trusts once, and never
// again. Round fifteen already made a v0.0.6 certificate re-issue on first boot
// because its SANs were incomplete, so that ceremony was owed regardless; this
// makes it the last one.
func ensureSelfSignedCert(dir, addr string) (string, string, error) {
	caCert := filepath.Join(dir, "tls-ca.pem")
	caKey := filepath.Join(dir, "tls-ca-key.pem")
	certFile := filepath.Join(dir, "tls-cert.pem")
	keyFile := filepath.Join(dir, "tls-key.pem")

	ca, caSigner, err := ensureCA(caCert, caKey)
	if err != nil {
		return "", "", err
	}
	// The leaf is checked against the CA it must chain to, so a leaf left over
	// from a previous CA is replaced rather than served unverifiably.
	if usableLeaf(certFile, keyFile, addr, ca) {
		return certFile, keyFile, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := newSerial()
	if err != nil {
		return "", "", err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "dibs"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	// ONE list, shared with the reuse check, so a name added here makes every
	// leaf lacking it renewable instead of quietly outliving the rule.
	//
	// A WILDCARD bind needs the addresses a client will actually dial: the
	// wizard's "this machine and others" writes 0.0.0.0, `dibs mcp-config`
	// correctly refuses to hand anybody a listen address and substitutes this
	// machine's LAN address, and a certificate that does not name it cannot be
	// verified. The interfaces are enumerated at generation time; a machine
	// that later moves networks gets a new LEAF, which now costs nothing
	// because the pinned CA above it does not change.
	for _, n := range certNames(addr) {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, ca, &key.PublicKey, caSigner)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	// KEY FIRST, then the certificate.
	//
	// These are two files and the pair is only valid together, so an interrupted
	// rotation leaves a mismatch either way. Writing the key first makes the
	// surviving mismatch "old certificate, new key", which usableLeaf rejects
	// on the chain check and regenerates. usableCert used to check only that
	// the key FILE existed, so it declared any mismatch usable, ServeTLS then
	// failed at startup, and the predicate responsible for regenerating kept
	// saying there was nothing to regenerate: a daemon that could not repair
	// itself from its own data directory.
	if err := writePEM(keyFile, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return "", "", err
	}
	// The chain, leaf first: a client that has pinned the CA verifies the leaf
	// through it, and one that has pinned nothing sees the whole path.
	if err := writePEMChain(certFile, der, ca.Raw); err != nil {
		return "", "", err
	}
	return certFile, keyFile, nil
}

// ensureCA returns the board's long-lived signing identity, generating it once.
//
// This is the thing `dibs trust` pins, so it must outlive every leaf and must
// not depend on any address: a board that changes networks, renews, or gains an
// interface keeps the same CA and every machine that trusted it stays working.
func ensureCA(caCert, caKey string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	pair, loadErr := tls.LoadX509KeyPair(caCert, caKey)
	if loadErr == nil {
		cert, perr := x509.ParseCertificate(pair.Certificate[0])
		signer, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
		switch {
		case perr != nil || !ok:
			return nil, nil, fmt.Errorf("the board's signing identity at %s cannot be "+
				"read (%v). Every machine that ran `dibs trust` against this board is "+
				"pinned to it, so this daemon will not quietly issue a new one: "+
				"restore the file, or delete %s and %s and re-trust each of those "+
				"machines once", caCert, perr, caCert, caKey)
		case !time.Now().Before(cert.NotAfter):
			return nil, nil, fmt.Errorf("the board's signing identity at %s expired on "+
				"%s. Delete it and %s, restart, and run `dibs trust` again on every "+
				"machine that joined this board: a new identity is not something this "+
				"daemon may choose on their behalf",
				caCert, cert.NotAfter.Format("2006-01-02"), caKey)
		}
		return cert, signer, nil
	}
	// GENERATED ONLY WHEN THERE IS NOTHING THERE.
	//
	// This fell through to generation for every failure: missing, unreadable,
	// malformed, mismatched, wrong key type, near expiry. Each of those quietly
	// replaced the identity every joined machine has pinned, so a corrupted
	// file or a bad restore would silently lock out the whole fleet while the
	// daemon reported itself healthy, and the README promised the identity
	// changes only when the operator deletes it. Absent means first run and is
	// the one case worth guessing at; anything else is the operator's call,
	// because only they can redo the ceremony on the other machines.
	// EITHER FILE. This asked only about the certificate, so a surviving KEY
	// beside a missing certificate, which is what an interrupted restore or a
	// half-finished deletion leaves, fell through and overwrote the key: the
	// identity every joined machine has pinned, replaced by a daemon that then
	// reported itself healthy. Half a pair present is the state that most
	// needs a person, because it is the one where the other half may still be
	// recoverable.
	for _, f := range []string{caCert, caKey} {
		if _, statErr := os.Stat(f); statErr != nil {
			continue
		}
		return nil, nil, fmt.Errorf("the board's signing identity is half here: %s "+
			"exists and the pair (%s, %s) cannot be loaded (%v). Machines that "+
			"trusted this board are pinned to that identity, so replacing it is not "+
			"this daemon's to decide: restore the missing half, or delete both and "+
			"run `dibs trust` again on every machine that joined",
			f, caCert, caKey, loadErr)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "dibs board CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	if err := writePEM(caKey, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return nil, nil, err
	}
	if err := writePEM(caCert, "CERTIFICATE", der, 0o644); err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func newSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	b := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	return os.WriteFile(path, b, mode)
}

// superviseSettings turns the [supervise] table into what the engine wants.
//
// The overlay lives in internal/liveness so `dibs probe` applies the SAME file
// the daemon does: the two used to diverge, with the CLI reachable only by
// flags.
func superviseSettings(c liveness.Settings) engine.SuperviseSettings {
	return engine.SuperviseSettings{
		Config: c.Apply(liveness.DefaultConfig()),
		Every:  c.Cadence(),
	}
}

// certLifetime is bounded by what CLIENTS will accept, not by what is
// convenient to reissue.
//
// This was ten years, and every Apple platform refuses a TLS server
// certificate whose validity exceeds 398 days: macOS reports it as "certificate
// is not standards compliant" and will not connect at all. So the one thing the
// self-signed path exists for, letting a second machine reach the daemon
// without anybody standing up a CA, did not work on the operating system this
// is mostly run from. Found by pointing a Mac at a daemon on another address
// and reading why it refused.
//
// 365 days, comfortably inside the cap and a round number a person can reason
// about, with rotation below so the shorter life is not a new failure.
const certLifetime = 365 * 24 * time.Hour

// certRenewBefore is how early a certificate is replaced. A cert that expires
// while the daemon is up would otherwise turn every agent on every other
// machine away at once, with nothing having changed on either side.
const certRenewBefore = 30 * 24 * time.Hour

// caLifetime is how long the pinned identity lasts. Long, deliberately: it is
// what `dibs trust` records on every joined machine, and replacing it costs a
// human a fingerprint ceremony on each of them. It signs and carries no
// addresses, so nothing about the machine can invalidate it.
const caLifetime = 10 * 365 * 24 * time.Hour

// usableLeaf reports whether the stored pair can still be served FOR addr, as
// a leaf of ca.
//
// The old check was os.Stat on both files, which answers "does a certificate
// exist" and never "is it any good". With a ten-year life that was nearly the
// same question. With a bounded life it is not: an expired certificate on disk
// would be served forever, and the failure surfaces on the CLIENTS, all of
// them, at once.
//
// AND EXPIRY IS NOT THE ONLY WAY A CERTIFICATE STOPS WORKING. It also stops
// working when it no longer names the address clients dial, which is not
// hypothetical: the SANs a wildcard bind needs were added in this release, so
// every board upgrading from v0.0.6 holds a still-valid certificate covering
// 0.0.0.0 and localhost and nothing else. `dibs mcp-config` then correctly
// hands out the LAN address, the daemon starts, and every client fails
// hostname verification. A machine that changes networks lands in the same
// place. Both are silent: the daemon is healthy and only the clients see it.
//
// So the question is whether this certificate covers what a fresh one would.
// If not it is regenerated, which is the same answer expiry already gets.
func usableLeaf(certFile, keyFile, addr string, ca *x509.Certificate) bool {
	// THE PAIR, not the two files. Replacement writes the certificate and the
	// key separately, certificate first, so a crash between them leaves a new
	// certificate beside an old key. Checking only that the key EXISTS declared
	// that pair usable on every later boot, ServeTLS then failed to load it and
	// the daemon exited, and the predicate whose job is deciding whether to
	// regenerate kept saying there was nothing to regenerate. A daemon that
	// cannot repair itself from its own data directory is worse than one that
	// never wrote the file.
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		return false
	}
	pemBytes, err := os.ReadFile(certFile) // #nosec G304 -- the daemon's own data directory
	if err != nil {
		return false
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	if !time.Now().Before(cert.NotAfter.Add(-certRenewBefore)) {
		return false
	}
	// AND IT MUST CHAIN TO THE CA WE HOLD. A leaf left behind by a previous CA
	// parses, is unexpired and names the right addresses, and no client that
	// trusts the current CA can verify it. Checking the chain is also what
	// makes an interrupted rotation self-repairing.
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		slog.Info("the stored certificate does not chain to this board's CA; "+
			"generating a new one", "err", err)
		return false
	}
	return covers(cert, addr)
}

// writePEMChain writes the leaf followed by the certificates above it, so a
// client that has pinned the CA can build the path.
func writePEMChain(path string, ders ...[]byte) error {
	var b []byte
	for _, der := range ders {
		b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	return os.WriteFile(path, b, 0o644) // #nosec G306 -- a public certificate chain
}

// covers reports whether cert names every address a fresh one would name for
// this bind.
//
// Compared against what generation WOULD produce rather than against a fixed
// list, so the two cannot drift: adding a name to the template automatically
// makes every certificate lacking it renewable.
func covers(cert *x509.Certificate, addr string) bool {
	have := map[string]bool{}
	for _, ip := range cert.IPAddresses {
		have[ip.String()] = true
	}
	for _, d := range cert.DNSNames {
		have[d] = true
	}
	for _, want := range certNames(addr) {
		if !have[want] {
			slog.Info("the stored certificate does not name an address clients dial; "+
				"generating a new one", "missing", want, "addr", addr)
			return false
		}
	}
	return true
}

// certNames is every name a self-signed certificate for this bind must carry.
//
// Shared by generation and by the reuse check, and that is the point: the two
// disagreed, so a board that upgraded kept a still-valid certificate from
// before the wildcard SANs existed and served it to clients that could not
// verify it. One list means adding a name here renews every certificate that
// lacks it, and there is no second place to remember.
func certNames(addr string) []string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	names := []string{"localhost", "127.0.0.1", net.IPv6loopback.String()}
	if ip := net.ParseIP(host); ip != nil {
		names = append(names, ip.String())
	} else if host != "" {
		names = append(names, host)
	}
	for _, ip := range localAddresses(host) {
		names = append(names, ip.String())
	}
	return names
}

// localAddresses is every address of this machine a client could dial, for a
// certificate that has to cover a wildcard bind.
//
// Empty unless the bind IS a wildcard: a certificate should name what it serves
// and nothing more, and a daemon told to listen on one address has already said
// which one.
func localAddresses(host string) []net.IP {
	switch host {
	case "0.0.0.0", "::", "[::]", "":
	default:
		return nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, n.IP)
	}
	return out
}
