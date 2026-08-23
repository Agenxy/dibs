package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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
func ensureSelfSignedCert(dir, addr string) (string, string, error) {
	certFile := filepath.Join(dir, "tls-cert.pem")
	keyFile := filepath.Join(dir, "tls-key.pem")
	if usableCert(certFile, keyFile, addr) {
		return certFile, keyFile, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "dibs"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(certLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	// ONE list, shared with the reuse check, so a name added here makes every
	// certificate lacking it renewable instead of quietly outliving the rule.
	for _, n := range certNames(addr) {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	// A WILDCARD bind needs the addresses a client will actually dial.
	//
	// The wizard's "this machine and others" writes 0.0.0.0, so the certificate
	// carried IP SAN 0.0.0.0 and DNS SAN localhost, and nothing else. Then
	// `dibs mcp-config` correctly refuses to hand anybody 0.0.0.0, which is a
	// listen address, and substitutes this machine's LAN address instead. That
	// address is not in the certificate, so verification fails and the operator
	// is handed a configuration that cannot connect: one unusable answer traded
	// for another. Raised by the pre-release review.
	//
	// The interfaces are enumerated at generation time. A machine that later
	// moves networks needs a new certificate, which is true of any name in a
	// certificate and is why the file is regenerated rather than patched.
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	if err := writePEM(certFile, "CERTIFICATE", der, 0o644); err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	if err := writePEM(keyFile, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return "", "", err
	}
	return certFile, keyFile, nil
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

// usableCert reports whether the stored pair can still be served FOR addr.
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
func usableCert(certFile, keyFile, addr string) bool {
	if _, err := os.Stat(keyFile); err != nil {
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
	return covers(cert, addr)
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
