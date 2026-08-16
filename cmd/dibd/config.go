package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/liveness"
)

// Config is everything dibd can be told. Every field is optional: running
// `dibd` with no arguments and no config file must always do the right thing.
// Written as <dir>/dibs.toml for people who want to get their hands dirty.
type Config struct {
	Addr              string            `toml:"addr"`               // listen address
	TLSCert           string            `toml:"tls_cert"`           // explicit cert (else auto)
	TLSKey            string            `toml:"tls_key"`            //
	InsecurePlaintext bool              `toml:"insecure_plaintext"` // force plaintext off-loopback
	Match             MatchConfig       `toml:"match"`              // work-overlap matching
	Limits            LimitsConfig      `toml:"limits"`             // coordination timings
	Supervise         liveness.Settings `toml:"supervise"`          // stalled-subagent detection
	Roles             RolesConfig       `toml:"roles"`              // standing coordinator/admin agents
	Wake              WakeConfig        `toml:"wake"`               // which news may extend an agent's turn
}

// WakeConfig is the [wake] table.
//
// One key, because there is one real decision: an FYI arriving at the end of a
// turn either extends that turn or waits for the next activation. The default
// waits, because extending a finished turn to hand an agent something nobody is
// waiting on is Dibs driving a harness.
//
// `all` exists for an UNATTENDED fleet. A queued message reaches an agent at
// its next activation, and an agent nobody prompts may not have one for hours,
// so on a machine running without a person the queue is where mail waits. That
// is an operator's call about their own fleet, not something a default can know.
type WakeConfig struct {
	// ExtendTurnFor is "urgent" (default), "all", or "none".
	ExtendTurnFor string `toml:"extend_turn_for"`
}

// policy validates the setting and returns it.
func (w WakeConfig) policy() (engine.WakePhase, error) {
	switch w.ExtendTurnFor {
	case "":
		return engine.WakeUrgent, nil
	case string(engine.WakeUrgent), string(engine.WakeAll), string(engine.WakeNone):
		return engine.WakePhase(w.ExtendTurnFor), nil
	default:
		return "", fmt.Errorf("[wake] extend_turn_for = %q: use \"urgent\" (only work "+
			"somebody is waiting on), \"all\" (anything unread, for an unattended fleet) "+
			"or \"none\"", w.ExtendTurnFor)
	}
}

// LimitsConfig is the [limits] table: the timings an operator's fleet actually
// changes. Everything else in core.Limits is a safety bound rather than a
// preference, and is deliberately not exposed.
type LimitsConfig struct {
	// AgentTTL is how long an agent may go silent before it is treated as
	// crashed. It matters more than it looks: a stale owner YIELDS its
	// exclusive agents, so an agent that is merely busy: a long build, a slow
	// tool call, a big test run makes no Dibs calls for its duration: can be
	// declared dead and lose an agent it is still working in.
	//
	// The default of 5m suits chatty agents. Fleets that run long silent steps
	// want more; fleets that want fast crash detection want less. There is no
	// value that is right for both, which is why this is a knob and not a
	// better constant.
	AgentTTL string `toml:"agent_ttl"`

	// IdleTTL is agent_ttl's counterpart for agents that gave no PID, where
	// silence is the only evidence available.
	//
	// It needs its own knob because it governs the configuration Dibs itself
	// tells people to use: `dibs mcp-config` prints a plain HTTP client, which
	// registers without a pid, so an operator who sets agent_ttl and points that
	// client at the daemon changes nothing and waits 45 minutes for a lapse
	// they thought they had configured to 5.
	IdleTTL string `toml:"idle_ttl"`

	// BlobStoreBytes caps the attachment store. It is a HARD bound: when the
	// store is over it, eviction drops referenced content rather than exceed
	// it, so a recipient can end up holding a message that names a blob which
	// is gone. Fleets that exchange large artifacts want this bigger; a machine
	// with little disk to spare wants it smaller. Accepts a plain byte count.
	BlobStoreBytes int64 `toml:"blob_store_bytes"`
}

// MatchConfig is the [match] table (SPEC-CHANNELS.md §7).
//
// It belongs in a file rather than only in flags because the two numbers that
// matter are MEASURED, not chosen: `dibs calibrate` prints thresholds for a
// specific repository, and retyping them onto a command line every restart is
// how a carefully calibrated bar quietly becomes a guess again.
//
// Every field is optional and a flag always wins, so an operator can override
// one setting for one run without editing anything.
type MatchConfig struct {
	// Repo enables matching. Empty means off: an index built from the wrong
	// repository is worse than none, so this is never inferred.
	Repo string `toml:"repo"`
	// Join is the auto-join threshold. 0 means suggest only. There is no safe
	// default: it is unitless and scorer-relative, so run `dibs calibrate`.
	Join float64 `toml:"join_threshold"`
	// Notify is the mention threshold.
	Notify float64 `toml:"notify_threshold"`
	// History bounds the co-change mining.
	History int `toml:"history"`
	// Deadline bounds the scorer; declaring work never blocks on it.
	Deadline string `toml:"deadline"`
	// EmbedURL points at a tier-2/3 embedding service (contrib/embed-sidecar).
	EmbedURL string `toml:"embed_url"`
	// EmbedModel is the model to request from it.
	//
	// Every other match setting could live here and this one could not, so an
	// operator using an embedding service had to retype `-match-embed-model` on
	// every restart while the URL beside it sat in the file. That is precisely
	// what this table exists to stop.
	//
	// There is deliberately no embed_key: a bearer token belongs in the
	// environment (DIBS_MATCH_EMBED_KEY), not in a file people paste into
	// issues and commit by accident.
	EmbedModel string `toml:"embed_model"`
	// EmbedQueryPrefix / EmbedDocPrefix override the retrieval markers Dibs
	// infers from the model name.
	//
	// Retrieval models are asymmetric and every family marks its two sides
	// differently. Dibs knows the common conventions, but a model it has not
	// heard of gets none, and measured on this repository, a model addressed
	// without its markers separates related from unrelated work about half as
	// well. Set these when you know the convention Dibs does not.
	EmbedQueryPrefix string `toml:"embed_query_prefix"`
	EmbedDocPrefix   string `toml:"embed_doc_prefix"`
	// DirectorRequired gates joins on a coordinator admitting them. Off by
	// default: it serialises the fleet behind one agent.
	DirectorRequired bool `toml:"director_required"`

	// AutoJoin: "declared" (default), "always", or "never".
	//
	// Who decides whether a match becomes a membership. The default lets Dibs
	// join you only on DECLARED overlap: both agents named the same ref, which is
	// a fact rather than a similarity score, and offers everything else as a
	// proposal for the agent to judge. See engine.MatchConfig.AutoJoin for why
	// that turned out to be the right split.
	AutoJoin string `toml:"auto_join"`
}

// apply folds the [limits] table over the defaults.
//
// A bad value is an ERROR, never a silent fallback: an operator who wrote
// agent_ttl = "10" (meaning minutes, in a field that takes a duration) and got
// the 5-minute default back would be debugging phantom crashes with no idea the
// setting had been ignored.
func (c LimitsConfig) apply(base core.Limits) (core.Limits, error) {
	if err := applyTTL("agent_ttl", c.AgentTTL, &base.AgentTTL); err != nil {
		return base, err
	}
	if err := applyTTL("idle_ttl", c.IdleTTL, &base.IdleTTL); err != nil {
		return base, err
	}
	return base, c.applyBlobCap(&base)
}

// applyTTL parses one duration limit, refusing anything unusable.
//
// A bad value is an ERROR, never a silent fallback, and the floor is enforced
// for the same reason: agents renew at half the TTL, so anything shorter marks
// healthy agents crashed between their own keepalives: a fleet-wide phantom
// outage produced by a setting that looked accepted.
func applyTTL(key, raw string, dst *time.Duration) error {
	if raw == "" {
		return nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf(
			"[limits] %s = %q is not a duration: write it like \"5m\", \"90s\" or \"1h\": %w",
			key, raw, err,
		)
	}
	if d < minAgentTTL {
		return fmt.Errorf(
			"[limits] %s = %q is below the %s floor: agents renew their lease at "+
				"half the TTL, so anything shorter marks healthy agents crashed between "+
				"their own keepalives", key, raw, minAgentTTL,
		)
	}
	*dst = d
	return nil
}

// applyBlobCap folds blob_store_bytes over the default, refusing anything that
// could not hold a single maximum-sized blob: a store smaller than one item
// evicts everything the moment it is used, which looks like attachments simply
// not working.
func (c LimitsConfig) applyBlobCap(base *core.Limits) error {
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
const minAgentTTL = 5 * time.Second

// loadConfig reads <dir>/dibs.toml if present. A missing file is not an error,
// zero config is the supported default, not a degraded mode.
func loadConfig(dir string) (Config, error) {
	var c Config
	// #nosec G304 -- a path inside the daemon's own data directory, or one the
	// operator pointed the CLI at. Same-user access only; refusing it would mean
	// refusing to run.
	b, err := os.ReadFile(filepath.Join(dir, "dibs.toml"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	md, err := toml.Decode(string(b), &c)
	if err != nil {
		return c, err
	}
	// A key TOML did not recognise is a key that did nothing.
	//
	// Silently ignoring it is the worst outcome available: `[limit]` for
	// `[limits]`, or `agent_ttl` under `[match]`, parses cleanly, changes
	// nothing, and leaves the operator certain they configured something. They
	// then debug the behaviour they were trying to change. Decode reports what
	// it could not place, so say so and name it.
	if un := md.Undecoded(); len(un) > 0 {
		keys := make([]string, 0, len(un))
		for _, k := range un {
			keys = append(keys, k.String())
		}
		return c, fmt.Errorf(
			"unknown setting(s) in dibs.toml: %s: check the spelling and the table "+
				"they are under ([match], [limits]); nothing here took effect",
			strings.Join(keys, ", "),
		)
	}
	return c, nil
}

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
// or a certificate authority to be safe by default. Sovereign, not dependent.
func resolveTransport(dir, addr string, c Config) (transport, error) {
	if c.TLSCert != "" && c.TLSKey != "" {
		return transport{c.TLSCert, c.TLSKey, "TLS (certificate from config)"}, nil
	}
	if isLoopbackAddr(addr) {
		return transport{why: "plaintext (loopback: unreachable from other hosts)"}, nil
	}
	if c.InsecurePlaintext {
		return transport{why: "plaintext (insecure_plaintext set in config: you accepted this)"}, nil
	}
	cert, key, err := ensureSelfSignedCert(dir, addr)
	if err != nil {
		return transport{}, fmt.Errorf("could not prepare TLS for %s: %w", addr, err)
	}
	return transport{cert, key, "TLS (self-signed certificate, auto-generated)"}, nil
}

// ensureSelfSignedCert returns a cert/key for addr, generating them into the
// data dir on first use so the operator never has to think about certificates.
func ensureSelfSignedCert(dir, addr string) (string, string, error) {
	certFile := filepath.Join(dir, "tls-cert.pem")
	keyFile := filepath.Join(dir, "tls-key.pem")
	if usableCert(certFile, keyFile) {
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
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		host = addr
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
		DNSNames:              []string{"localhost"},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else if host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
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

// usableCert reports whether the stored pair can still be served.
//
// The old check was os.Stat on both files, which answers "does a certificate
// exist" and never "is it any good". With a ten-year life that was nearly the
// same question. With a bounded life it is not: an expired certificate on disk
// would be served forever, and the failure surfaces on the CLIENTS, all of
// them, at once.
func usableCert(certFile, keyFile string) bool {
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
	return time.Now().Before(cert.NotAfter.Add(-certRenewBefore))
}
