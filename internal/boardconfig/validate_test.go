package boardconfig

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
)

// A value the daemon refuses must not load here.
//
// Decoding and unknown keys are not the whole of what makes a configuration
// unusable. `agent_ttl = "10"` is a string, decodes cleanly, and is not a
// duration; `extend_turn_for = "everything"` names no policy. Both stopped the
// daemon while `dibs mcp-config` printed a complete configuration around them
// and exited 0: the same success-that-is-false this package was created to
// end, found one round after it was created. Raised by the pre-release review.
func TestLoadRefusesValuesTheDaemonRefuses(t *testing.T) {
	bad := []struct{ name, body string }{
		{"a duration that is not one", "[limits]\nagent_ttl = \"10\"\n"},
		{"a duration below the floor", "[limits]\nagent_ttl = \"1s\"\n"},
		{"an idle ttl that is not a duration", "[limits]\nidle_ttl = \"soon\"\n"},
		{"a negative ceiling", "[limits]\nmax_agents = -1\n"},
		{"a wake policy that names nothing", "[wake]\nextend_turn_for = \"everything\"\n"},
		{"a persistent ceiling above the total", "[limits]\nmax_agents = 4\nmax_persistent_agents = 9\n"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil {
				t.Errorf("loaded a file the daemon will not start on:\n%s", c.body)
			}
		})
	}

	good := []struct{ name, body string }{
		{"the empty file", ""},
		{"a real duration", "[limits]\nagent_ttl = \"10m\"\n"},
		{"every wake policy", "[wake]\nextend_turn_for = \"urgent\"\n"},
		{
			"one ceiling alone, which the daemon compares against its own default",
			"[limits]\nmax_persistent_agents = 9\n",
		},
	}
	for _, c := range good {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err != nil {
				t.Errorf("refused a usable configuration: %v\n%s", err, c.body)
			}
		})
	}

	// A missing file is the ordinary case and stays silent.
	if _, err := Load(t.TempDir()); err != nil {
		t.Errorf("a data directory with no dibs.toml was refused: %v", err)
	}
}

// The error has to name the setting, or an operator is told their file is bad
// and not which line of it.
func TestTheRefusalNamesTheSetting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"),
		[]byte("[wake]\nextend_turn_for = \"everything\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("accepted an unknown wake policy")
	}
	for _, want := range []string{"extend_turn_for", "everything", "urgent"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// Settings the daemon refuses or silently ignores must not load here either.
//
// Round two moved the checks in with the type; round four found the list was
// short. Each of these produced a complete-looking configuration around a
// setting that never took effect, which is the pattern this package exists to
// end. Raised by the pre-release review.
func TestLoadRefusesSettingsThatWouldNotTakeEffect(t *testing.T) {
	bad := []struct{ name, body string }{
		{
			"a listen address carrying a scheme, which net.Listen cannot bind",
			"addr = \"https://127.0.0.1:4777\"\n",
		},
		{"a listen address that is not host:port", "addr = \"127.0.0.1\"\n"},
		// SplitHostPort parses these and net.Listen refuses them, and `dibd
		// -check` never attempts the bind: it is what an operator runs before
		// stopping the daemon being replaced.
		{"a port that is not a number", "addr = \"hub:not-a-port\"\n"},
		{"a port above 65535", "addr = \"127.0.0.1:99999\"\n"},
		{"port zero, which binds something arbitrary", "addr = \"127.0.0.1:0\"\n"},
		{"a certificate with no key", "tls_cert = \"/c.pem\"\n"},
		{"a key with no certificate", "tls_key = \"/k.pem\"\n"},
		{"a negative blob store", "[limits]\nblob_store_bytes = -1\n"},
		{"a negative match history", "[match]\nhistory = -1\n"},
		{"a match deadline that is not a duration", "[match]\ndeadline = \"soon\"\n"},
		{"an auto_join value that names nothing", "[match]\nauto_join = \"maybe\"\n"},
		{"a negative supervision interval", "[supervise]\nevery = \"-5m\"\n"},
		// min_duty is a fraction, and round six found BOTH ends unchecked while
		// this very list claimed to cover settings that do not take effect.
		// Negative is the ordinary silent-default case. Above 1 is the
		// dangerous one: the duty check ACQUITS a process that clears the
		// threshold, so a threshold nothing can clear acquits nobody and every
		// process past min_age becomes eligible for a stuck verdict.
		{"a negative duty fraction, which is ignored", "[supervise]\nmin_duty = -0.1\n"},
		{"a duty fraction above 1, which convicts everything", "[supervise]\nmin_duty = 1.5\n"},
		// Round seven: explicit zeros validated and were then ignored, which is
		// this list's own subject. Settings.Apply overlays only values above
		// zero, so `every = "0s"` reads as "supervise constantly" and silently
		// keeps the default interval.
		{"a zero supervision interval, which is ignored", "[supervise]\nevery = \"0s\"\n"},
		{"a zero quiet window, which is ignored", "[supervise]\nquiet = \"0s\"\n"},
		{"a zero minimum age, which is ignored", "[supervise]\nmin_age = \"0s\"\n"},
		{"a zero duty fraction, which is ignored", "[supervise]\nmin_duty = 0.0\n"},
		// NaN passed `< 0 || > 1` because no comparison against NaN is true,
		// and was then ignored by the `> 0` test on the way in. TOML has NaN.
		{"a duty fraction of nan, which no comparison catches", "[supervise]\nmin_duty = nan\n"},
		// The two the shared loader accepted while the daemon refused them, which
		// is the one failure this loader exists to make impossible: `dibs
		// mcp-config` printed a complete configuration and exited zero for a
		// board that cannot boot. Both need the DEFAULT for the other side of
		// the comparison, and the omission was deliberate ("not ours to know").
		{
			"a persistent ceiling above the DEFAULT max_agents",
			"[limits]\nmax_persistent_agents = 65\n",
		},
		{
			"a blob store too small for one maximum-sized blob",
			"[limits]\nblob_store_bytes = 1024\n",
		},
		// [wake.exec] arrived with this list's own subject in it: cooldown took
		// any duration and the waker maps everything <= 0 to the 90s default,
		// so a negative one passed `dibd -check`, startup reported the harness
		// configured, and the operator's explicit value did nothing. Zero is
		// documented as "take the default" and stays legal.
		{
			"a negative wake cooldown, which silently becomes the default",
			"[wake.exec.codex]\nargv = [\"codex\"]\ncooldown = \"-1s\"\n",
		},
		{
			"a wake command whose executable is the empty string",
			"[wake.exec.codex]\nargv = [\"\", \"exec\"]\n",
		},
		// " " is a valid TOML string and a useless program name: it passed the
		// check, startup announced a configured harness, and every wake failed
		// inside exec before starting anything.
		{
			"a wake command whose executable is only whitespace",
			"[wake.exec.codex]\nargv = [\" \", \"exec\"]\n",
		},
		{
			"a wake entry whose harness name is blank",
			"[wake.exec.\" \"]\nargv = [\"/bin/echo\"]\n",
		},
		// NEITHER key is the canonical form, which is the collision the first
		// version could not see: it looked only for an exact lowercase peer.
		{
			"two harness keys that differ only in case",
			"[wake.exec.Codex]\nargv = [\"/bin/echo\"]\n\n[wake.exec.CODEX]\nargv = [\"/bin/true\"]\n",
		},
		// An entry with no argv at all: the section loaded, startup took the
		// "there is a wake command" branch, skipped the entry for want of an
		// argv, and logged `harnesses=0` as though that were a capability.
		{
			"a wake entry with no argv, which can start nothing",
			"[wake.exec.codex]\nargv = []\n",
		},
		{
			"a wake entry that sets only a cooldown",
			"[wake.exec.codex]\ncooldown = \"2m\"\n",
		},
		// The identity table is a CREDENTIAL-shaped setting, and the first
		// version of the feature asked for the nonce itself. A nonce in a file
		// every same-user process can read is that process's route to
		// reattaching AS the admin.
		{
			"a role identity given as a raw nonce rather than a fingerprint",
			"[roles]\nadmin = [\"fleet-lead\"]\n[roles.identity]\nfleet-lead = \"the-secret-nonce\"\n",
		},
		{
			"a role identity for a name no role is declared for",
			"[roles]\nadmin = [\"fleet-lead\"]\n[roles.identity]\nsomebody-else = \"" + strings.Repeat("a", 64) + "\"\n",
		},
		// One agent holds ONE role, so naming it twice makes the reconciler
		// flip it back and forth for the whole startup window.
		{
			"one agent named as both coordinator and admin",
			"[roles]\ncoordinator = [\"fleet-lead\"]\nadmin = [\"fleet-lead\"]\n",
		},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil {
				t.Errorf("loaded a setting the daemon refuses or ignores:\n%s", c.body)
			}
		})
	}

	// A pair that does not exist is not a pair. `dibd -check` is what an
	// operator runs before stopping the old daemon during a takeover, and this
	// list used to declare /c.pem and /k.pem a "complete certificate pair": the
	// check said yes, the old daemon stopped, and the replacement failed inside
	// ServeTLS with the port already released.
	certPath, keyPath := writeTestPair(t)
	t.Run("tls paths that name nothing", func(t *testing.T) {
		dir := t.TempDir()
		body := "tls_cert = \"/c.pem\"\ntls_key = \"/k.pem\"\n"
		if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Error("a certificate pair that does not exist was accepted as a " +
				"complete transport, so `dibd -check` reports a board that cannot start")
		}
	})

	// And the shapes that are legitimate must still load.
	good := []struct{ name, body string }{
		{"a bare host:port", "addr = \"0.0.0.0:4777\"\n"},
		{"a complete certificate pair", "tls_cert = \"" + certPath + "\"\ntls_key = \"" + keyPath + "\"\n"},
		{"a real match deadline", "[match]\ndeadline = \"5m\"\n"},
		{"auto_join always", "[match]\nauto_join = \"always\"\n"},
		{"auto_join never", "[match]\nauto_join = \"never\"\n"},
		{"auto_join declared", "[match]\nauto_join = \"declared\"\n"},
	}
	for _, c := range good {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(c.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err != nil {
				t.Errorf("refused a usable configuration: %v\n%s", err, c.body)
			}
		})
	}
}

// writeTestPair generates a real certificate and key, because "a complete
// certificate pair" now means one that loads.
func writeTestPair(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	write := func(path, typ string, b []byte, mode os.FileMode) {
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b}), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(certPath, "CERTIFICATE", der, 0o644)
	write(keyPath, "EC PRIVATE KEY", keyDER, 0o600)
	return certPath, keyPath
}

// A certificate for the address the daemon will actually bind must load.
//
// `-addr` and DIBS_ADDR both outrank `addr` in dibs.toml, and this layer sees
// only the file. Verifying the hostname here therefore refused a pair that is
// CORRECT for the address the daemon was told to serve: the board did not start
// at all, at the layer with the least information about which address wins.
// That is worse than the hole it was closing, and cmd/dibd asks the same
// question after resolving, for both startup and `-check`.
//
// What this layer still owes: the pair loads, matches, and is in date.
func TestConfigLoadDoesNotJudgeTheHostname(t *testing.T) {
	cert, key := writeTestPair(t)
	c := Config{Addr: "10.0.0.9:4777", TLSCert: cert, TLSKey: key}
	if err := c.validateTLS(); err != nil {
		t.Errorf("a certificate that names no address was refused because dibs.toml "+
			"says 10.0.0.9. An operator starting this daemon with `-addr` elsewhere is "+
			"correct while the file is stale, and this layer cannot see which address "+
			"wins: %v", err)
	}
}
