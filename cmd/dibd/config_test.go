package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The [match] table exists because the two numbers that matter are MEASURED,
// not chosen: `dibs calibrate` prints thresholds for a specific repository,
// and retyping them onto a command line every restart is how a calibrated bar
// quietly becomes a guess again.
func TestMatchConfigIsReadFromFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(`
addr = "127.0.0.1:4999"

[match]
repo = "/tmp/somewhere"
join_threshold = 0.327
notify_threshold = 0.163
history = 500
deadline = "2s"
embed_url = "http://127.0.0.1:8737"
director_required = true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:4999" {
		t.Fatalf("addr = %q", c.Addr)
	}
	if c.Match.Repo != "/tmp/somewhere" || c.Match.Join != 0.327 || c.Match.Notify != 0.163 {
		t.Fatalf("match table not read: %+v", c.Match)
	}
	if c.Match.History != 500 || c.Match.EmbedURL == "" || !c.Match.DirectorRequired {
		t.Fatalf("match table incomplete: %+v", c.Match)
	}

	f := defaultScorerFlags() // as if nothing was passed on the command line
	f.applyConfig(c.Match)
	if f.repo != "/tmp/somewhere" || f.join != 0.327 || f.notify != 0.163 {
		t.Fatalf("config did not reach the scorer: %+v", f)
	}
	if f.history != 500 || f.embedURL == "" || !f.director {
		t.Fatalf("config did not reach the scorer: %+v", f)
	}
	if f.deadline != 2*time.Second {
		t.Fatalf("deadline = %v, want 2s", f.deadline)
	}
}

// A flag must win. Overriding one setting for one run cannot require editing
// the file everything else reads from.
func TestFlagsBeatTheConfigFile(t *testing.T) {
	f := defaultScorerFlags()
	f.repo, f.join, f.director = "/from/flag", 0.9, false
	f.deadline = 250 * time.Millisecond
	// What markSetFlags records when these appear on the command line. The test
	// used to imply "set" by assigning a non-zero value, which is precisely the
	// inference that made `-match-join 0` lose to a file: zero is a value.
	for _, k := range []string{"repo", "join", "director-required", "deadline"} {
		f.set[k] = true
	}
	f.applyConfig(MatchConfig{
		Repo: "/from/file", Join: 0.1, DirectorRequired: true, Deadline: "9s",
	})
	if f.repo != "/from/flag" {
		t.Errorf("repo = %q, the flag must win", f.repo)
	}
	if f.join != 0.9 {
		t.Errorf("join = %v, the flag must win", f.join)
	}
	if f.deadline != 250*time.Millisecond {
		t.Errorf("deadline = %v, the flag must win", f.deadline)
	}
	// This asymmetry used to be real and was documented here as deliberate: a
	// bool left false was indistinguishable from unset, so the file could only
	// ever turn it ON. It is no longer true. `set` records what was passed,
	// and an explicit `-match-director-required=false` now wins, which is what
	// an operator typing it plainly means.
	if f.director {
		t.Error("an explicitly-false bool flag must not be overridden by the file")
	}
}

// A missing file is not an error: zero config is the supported default, not a
// degraded mode.
func TestNoConfigFileIsFine(t *testing.T) {
	c, err := loadConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Match.Repo != "" || c.Match.Join != 0 {
		t.Fatalf("absent config must be zero, got %+v", c.Match)
	}
}

// An unparseable duration must warn and carry on, never refuse to start.
func TestBadDeadlineDoesNotStopTheDaemon(t *testing.T) {
	f := defaultScorerFlags()
	before := f.deadline
	f.applyConfig(MatchConfig{Deadline: "not-a-duration"})
	if f.deadline != before {
		t.Fatalf("a bad duration must leave the default alone, got %v", f.deadline)
	}
}

// AgentTTL decides when a silent agent is treated as crashed, and a stale owner
// YIELDS its exclusive agents, so an agent that is merely busy for longer than
// the TTL (a long build, a slow test run: no Dibs calls for its duration) is
// declared dead and loses an agent it is still working in. 5m suits chatty
// agents and nothing else, which is why it is a knob.
func TestAgentTTLIsConfigurable(t *testing.T) {
	base := core.DefaultLimits()
	got, err := LimitsConfig{AgentTTL: "20m"}.apply(base)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentTTL != 20*time.Minute {
		t.Fatalf("want 20m, got %s", got.AgentTTL)
	}
	// Everything else is a safety bound, not a preference, and must be untouched.
	if got.ClaimLease != base.ClaimLease || got.ArchiveRetention != base.ArchiveRetention {
		t.Fatal("[limits] must not disturb bounds it does not name")
	}
	if unset, err := (LimitsConfig{}).apply(base); err != nil || unset.AgentTTL != base.AgentTTL {
		t.Fatalf("an absent table keeps the defaults, got %s / %v", unset.AgentTTL, err)
	}
}

// A bad value must be an ERROR, never a silent fallback: an operator who wrote
// agent_ttl = "10" and got the 5-minute default back would be debugging phantom
// crashes with no idea the setting had been ignored.
func TestABadAgentTTLIsRefusedWithTheFix(t *testing.T) {
	for _, bad := range []string{"10", "soon", "-3m", "1s"} {
		_, err := LimitsConfig{AgentTTL: bad}.apply(core.DefaultLimits())
		if err == nil {
			t.Fatalf("agent_ttl = %q must be refused, not silently ignored", bad)
		}
		if !strings.Contains(err.Error(), "agent_ttl") {
			t.Fatalf("the error must name the setting, got: %v", err)
		}
	}
	// And the message has to show what a good value looks like.
	_, err := LimitsConfig{AgentTTL: "10"}.apply(core.DefaultLimits())
	if !strings.Contains(err.Error(), `"5m"`) {
		t.Fatalf("the error must show the shape it wants, got: %v", err)
	}
}

// A key TOML did not recognise is a key that did nothing, and silently ignoring
// it is the worst outcome available: `[limit]` for `[limits]`, or agent_ttl
// under `[match]`, parses cleanly, changes nothing, and leaves the operator
// certain they configured something. They then debug the behaviour they were
// trying to change, with the setting sitting right there in the file.
func TestUnknownConfigKeysAreRefusedNotIgnored(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"typo'd table", "[limit]\nagent_ttl = \"9m\"\n", "agent_ttl"},
		{"right key, wrong table", "[match]\nagent_ttl = \"9m\"\n", "agent_ttl"},
		{"misspelled key", "[limits]\nagent_tt1 = \"9m\"\n", "agent_tt1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadConfig(dir)
			if err == nil {
				t.Fatal("a setting that does nothing must say so, not start quietly")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the error must name the key that did nothing, got: %v", err)
			}
		})
	}
	// And a correct file still loads, or the check is worse than the bug.
	dir := t.TempDir()
	body := "addr = \"127.0.0.1:4999\"\n[match]\nrepo = \"/tmp\"\n[limits]\nagent_ttl = \"9m\"\n"
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("a valid file must load: %v", err)
	}
	if c.Addr != "127.0.0.1:4999" || c.Match.Repo != "/tmp" || c.Limits.AgentTTL != "9m" {
		t.Fatalf("every table must still decode: %+v", c)
	}
}

// The blob store cap is a HARD bound: over it, eviction drops referenced
// content rather than exceed it. An operator whose fleet exchanges large
// artifacts needs it bigger; one short on disk needs it smaller.
func TestBlobStoreCapIsConfigurable(t *testing.T) {
	base := core.DefaultLimits()
	got, err := LimitsConfig{BlobStoreBytes: 4 << 30}.apply(base)
	if err != nil {
		t.Fatal(err)
	}
	if got.BlobStoreBytes != 4<<30 {
		t.Fatalf("want 4GiB, got %d", got.BlobStoreBytes)
	}
	// Set alongside a agent_ttl, both must land: the early return for an absent
	// agent_ttl used to skip this one entirely.
	both, err := LimitsConfig{AgentTTL: "9m", BlobStoreBytes: 2 << 30}.apply(base)
	if err != nil {
		t.Fatal(err)
	}
	if both.AgentTTL != 9*time.Minute || both.BlobStoreBytes != 2<<30 {
		t.Fatalf("both settings must apply, got %s / %d", both.AgentTTL, both.BlobStoreBytes)
	}
	if unset, err := (LimitsConfig{AgentTTL: "9m"}).apply(base); err != nil ||
		unset.BlobStoreBytes != base.BlobStoreBytes {
		t.Fatalf("an absent cap keeps the default, got %d / %v", unset.BlobStoreBytes, err)
	}
}

// A store too small to hold one attachment evicts everything the moment it is
// used, which looks like attachments being broken rather than a cap doing its
// job. Refuse it at startup, where the operator can still see why.
func TestABlobStoreTooSmallToHoldOneBlobIsRefused(t *testing.T) {
	_, err := LimitsConfig{BlobStoreBytes: 1024}.apply(core.DefaultLimits())
	if err == nil {
		t.Fatal("a cap below one maximum blob must be refused")
	}
	if !strings.Contains(err.Error(), "blob_store_bytes") {
		t.Fatalf("the error must name the setting, got: %v", err)
	}
}

// Every match setting belongs in the file, or the file does not do its job.
//
// `-match-embed-model` had no toml key at all: an operator using an embedding
// service had to retype the model on every restart while the URL beside it sat
// in the config. Caught by the strict-key check refusing `match.embed_model`,
// which is the check working, turning a silent no-op into an error.
//
// There is deliberately NO embed_key: a bearer token belongs in the
// environment, not in a file people paste into issues.
func TestEveryMatchFlagHasAConfigKeyExceptTheSecret(t *testing.T) {
	dir := t.TempDir()
	body := `
[match]
repo = "/tmp/x"
join_threshold = 0.33
notify_threshold = 0.16
history = 500
deadline = "2s"
director_required = true
embed_url = "http://127.0.0.1:11434"
embed_model = "qwen3-embedding:0.6b"
embed_query_prefix = "Q: "
embed_doc_prefix = "D: "
`
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("every key here must be recognised: %v", err)
	}
	if c.Match.EmbedModel != "qwen3-embedding:0.6b" {
		t.Errorf("embed_model must load, got %q", c.Match.EmbedModel)
	}
	if c.Match.EmbedQueryPrefix != "Q: " || c.Match.EmbedDocPrefix != "D: " {
		t.Errorf("the retrieval markers must load, got %q/%q",
			c.Match.EmbedQueryPrefix, c.Match.EmbedDocPrefix)
	}

	// The secret is the one thing that must NOT be a config key.
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "dibs.toml"),
		[]byte("[match]\nembed_key = \"sk-secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(dir2); err == nil {
		t.Fatal("a bearer token must not be accepted from the config file")
	} else if !strings.Contains(err.Error(), "embed_key") {
		t.Fatalf("and the refusal must name it, got: %v", err)
	}
}

// Flags win over the file: the file is the durable default, the flag is what
// you are trying right now.
func TestAFlagOverridesTheConfigFile(t *testing.T) {
	f := &scorerFlags{embedModel: "from-flag", set: map[string]bool{"embed-model": true}}
	f.applyConfig(MatchConfig{EmbedModel: "from-file", EmbedQueryPrefix: "Q: "})
	if f.embedModel != "from-flag" {
		t.Errorf("an explicit flag must not be overwritten by the file, got %q", f.embedModel)
	}
	// And an unset flag takes the file's value, or the file is decoration.
	if f.embedQueryPrefix != "Q: " {
		t.Errorf("an unset flag must fall back to the file, got %q", f.embedQueryPrefix)
	}
}

// Precedence is flag > environment > file, and ZERO IS A VALUE.
//
// Two separate faults lived here. The comment said flag > env > file while the
// code did flag > file > env, because every env read happened after the file had
// already filled the slot. And once that was fixed, precedence was still decided
// by "is this still the zero value?", which silently makes 0 mean unset. For a
// threshold 0 is a real instruction; -match-join's own help says so ("0 =
// suggest only, never join"). So `-match-join 0` lost to a file's 0.5 and the
// daemon auto-joined against an explicit instruction not to.
//
// Both were found live rather than by reading, which is why this test drives
// applyConfig with the sources set the way an operator would set them.
func TestPrecedenceIsFlagThenEnvThenFile_AndZeroIsAValue(t *testing.T) {
	file := MatchConfig{Repo: "/from/file", Join: 0.5, Notify: 0.2, EmbedURL: "http://file"}

	t.Run("file alone", func(t *testing.T) {
		f := defaultScorerFlags()
		f.applyConfig(file)
		if f.repo != "/from/file" || f.join != 0.5 {
			t.Errorf("file values must apply when nothing else is set: repo=%q join=%v", f.repo, f.join)
		}
	})

	t.Run("env beats file", func(t *testing.T) {
		t.Setenv("DIBS_MATCH_REPO", "/from/env")
		t.Setenv("DIBS_MATCH_JOIN", "0.9")
		f := defaultScorerFlags()
		f.applyConfig(file)
		if f.repo != "/from/env" {
			t.Errorf("env repo must beat the file, got %q", f.repo)
		}
		if f.join != 0.9 {
			t.Errorf("env join must beat the file, got %v", f.join)
		}
	})

	t.Run("an explicit env zero beats a nonzero file", func(t *testing.T) {
		t.Setenv("DIBS_MATCH_JOIN", "0")
		f := defaultScorerFlags()
		f.applyConfig(file)
		if f.join != 0 {
			t.Errorf("DIBS_MATCH_JOIN=0 means 'never auto-join' and must win, got %v", f.join)
		}
	})

	t.Run("an explicit flag zero beats a nonzero env", func(t *testing.T) {
		t.Setenv("DIBS_MATCH_JOIN", "0.5")
		f := defaultScorerFlags()
		f.set["join"] = true // as markSetFlags records for `-match-join 0`
		f.join = 0
		f.applyConfig(file)
		if f.join != 0 {
			t.Errorf("-match-join 0 must beat both env and file, got %v", f.join)
		}
	})

	t.Run("an explicit empty prefix beats a file that names one", func(t *testing.T) {
		// A model documenting NO marker is configured by passing "": and
		// SetAffixes treats both-empty as "disable markers", not "detect again".
		withPrefix := file
		withPrefix.EmbedQueryPrefix = "query: "
		f := defaultScorerFlags()
		f.set["embed-query-prefix"] = true
		f.embedQueryPrefix = ""
		f.applyConfig(withPrefix)
		if f.embedQueryPrefix != "" {
			t.Errorf("an explicit empty marker must win, got %q", f.embedQueryPrefix)
		}
	})

	t.Run("an explicit default-valued duration still beats the file", func(t *testing.T) {
		// The nastiest form of zero-is-unset: the "zero" here is the DEFAULT
		// value, so `-match-deadline 1500ms`: a reasonable thing to type when
		// pinning behaviour: was indistinguishable from not passing it, and a
		// file's 9s silently won. Live, a request that should have timed out at
		// 1.5s ran for 2.2 seconds.
		withDeadline := file
		withDeadline.Deadline = "9s"
		f := defaultScorerFlags()
		f.set["deadline"] = true
		f.deadline = 1500 * time.Millisecond // exactly the default, passed on purpose
		f.applyConfig(withDeadline)
		if f.deadline != 1500*time.Millisecond {
			t.Errorf("an explicitly-passed default must still win, got %v", f.deadline)
		}
	})

	t.Run("an unset duration takes the file's", func(t *testing.T) {
		withDeadline := file
		withDeadline.Deadline = "9s"
		f := defaultScorerFlags()
		f.applyConfig(withDeadline)
		if f.deadline != 9*time.Second {
			t.Errorf("the file must apply when the flag is absent, got %v", f.deadline)
		}
	})

	t.Run("an explicit false bool beats a file true", func(t *testing.T) {
		withDirector := file
		withDirector.DirectorRequired = true
		f := defaultScorerFlags()
		f.set["director-required"] = true
		f.director = false
		f.applyConfig(withDirector)
		if f.director {
			t.Error("-match-director-required=false is an instruction and must win")
		}
	})
}

// idle_ttl is settable, and refused when unusable: like agent_ttl.
//
// It governs the configuration Dibs itself tells people to use: `agents
// mcp-config` prints a plain HTTP client, which registers without a PID, so an
// operator who set agent_ttl and pointed that client at the daemon changed
// nothing and waited 45 minutes for a lapse they thought they had configured to
// five. The knob existed in core.Limits and had no way in.
func TestIdleTTLIsConfigurableAndValidated(t *testing.T) {
	base := core.DefaultLimits()

	got, err := LimitsConfig{IdleTTL: "90s"}.apply(base)
	if err != nil {
		t.Fatalf("a valid idle_ttl was refused: %v", err)
	}
	if got.IdleTTL != 90*time.Second {
		t.Errorf("idle_ttl = %v, want 90s", got.IdleTTL)
	}
	// ...and agent_ttl is untouched by it.
	if got.AgentTTL != base.AgentTTL {
		t.Errorf("setting idle_ttl changed agent_ttl to %v", got.AgentTTL)
	}

	// Both keys at once, since they are independent knobs.
	both, err := LimitsConfig{AgentTTL: "2m", IdleTTL: "30m"}.apply(base)
	if err != nil {
		t.Fatalf("setting both was refused: %v", err)
	}
	if both.AgentTTL != 2*time.Minute || both.IdleTTL != 30*time.Minute {
		t.Errorf("got agent=%v idle=%v", both.AgentTTL, both.IdleTTL)
	}

	// A bad value is an ERROR, never a silent fallback: the same rule agent_ttl
	// follows, and for the same reason: a setting that looks accepted and was
	// ignored is debugged as phantom crashes.
	if _, err := (LimitsConfig{IdleTTL: "10"}).apply(base); err == nil {
		t.Error("idle_ttl = \"10\" is not a duration and must be refused, not defaulted")
	}
	if _, err := (LimitsConfig{IdleTTL: "1s"}).apply(base); err == nil {
		t.Error("idle_ttl below the floor must be refused; agents renew at half the TTL")
	}
}

// The daemon must honour DIBS_ADDR, and must not claim to be up before it is.
//
// Both were found by installing from a cold clone and following the README.
// DIBS_ADDR is read by every other Dibs binary; dibd ignored it and bound
// the default instead, so on a machine already running one the operator got
// "address already in use" for an address they never named.
//
// The second is worse than cosmetic. "dibd up" was logged before
// ListenAndServe, so a collision printed a confident success line and THEN the
// failure, and this project's own setup checks were fooled by exactly that
// twice, grepping for "dibd up", finding it, and going on to talk to a
// different daemon that already held the port.
func TestTheDaemonAnnouncesItselfOnlyAfterItHasThePort(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	if !strings.Contains(text, `os.Getenv("DIBS_ADDR")`) {
		t.Error("dibd does not read DIBS_ADDR, which every other Dibs binary honours")
	}

	bind := strings.Index(text, `net.Listen("tcp", listenAddr)`)
	announce := strings.Index(text, `slog.Info("dibd up"`)
	if bind < 0 {
		t.Fatal("dibd no longer binds explicitly: if it went back to ListenAndServe,\n" +
			"  it is once again claiming to be up before it has the port")
	}
	if announce < 0 {
		t.Fatal(`no "dibd up" line to order`)
	}
	if bind > announce {
		t.Error("dibd announces itself BEFORE binding, so a port collision prints\n" +
			"  success and then failure, which has already fooled this project's own\n" +
			"  setup checks twice")
	}
}

// The certificate must be one CLIENTS will accept, not merely one we can make.
//
// It was issued for ten years. Every Apple platform refuses a TLS server
// certificate whose validity exceeds 398 days, reporting it as "certificate is
// not standards compliant" and declining to connect at all, so the self-signed
// path existed to let a second machine reach the daemon and did not work on the
// operating system this is mostly run from. Found by pointing a Mac at a daemon
// on a routable address and reading why it refused.
func TestTheSelfSignedCertIsOneAppleWillAccept(t *testing.T) {
	const appleMaxDays = 398

	dir := t.TempDir()
	certFile, _, err := ensureSelfSignedCert(dir, "192.168.1.205:4777")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	cert := parseCert(t, certFile)

	days := int(cert.NotAfter.Sub(cert.NotBefore).Hours() / 24)
	if days > appleMaxDays {
		t.Errorf("certificate is valid for %d days; Apple refuses anything over %d, "+
			"so no macOS or iOS client can connect at all", days, appleMaxDays)
	}
	if days < 30 {
		t.Errorf("certificate is valid for only %d days: reissuing that often is its own outage", days)
	}
}

// An expired certificate on disk must not be served forever.
//
// The old check was os.Stat on both files, which answers "does one exist" and
// never "is it any good". That was nearly the same question at a ten-year life
// and is not at a bounded one: the failure would land on every client, on every
// other machine, at the same moment, with nothing having changed on either side.
func TestAnExpiringCertificateIsReplaced(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, err := ensureSelfSignedCert(dir, "192.168.1.205:4777")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	first := parseCert(t, certFile).SerialNumber.String()

	if !usableCert(certFile, keyFile) {
		t.Fatal("a freshly issued certificate was judged unusable")
	}

	// Rewind it to inside the renewal window by reissuing with a near NotAfter.
	writeCertExpiring(t, certFile, keyFile, time.Now().Add(24*time.Hour))
	if usableCert(certFile, keyFile) {
		t.Error("a certificate expiring tomorrow was judged usable, so it is served " +
			"until it fails on every client at once")
	}

	certFile2, _, err := ensureSelfSignedCert(dir, "192.168.1.205:4777")
	if err != nil {
		t.Fatal(err)
	}
	if parseCert(t, certFile2).SerialNumber.String() == first {
		t.Error("the expiring certificate was reused rather than replaced")
	}
}

func parseCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(blob)
	if block == nil {
		t.Fatalf("%s is not PEM", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// writeCertExpiring replaces the stored certificate with one expiring at t.
func writeCertExpiring(t *testing.T, certFile, keyFile string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "dibs"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keyFile); err != nil {
		t.Fatal(err)
	}
}
