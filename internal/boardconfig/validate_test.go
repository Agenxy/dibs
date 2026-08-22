package boardconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	// And the shapes that are legitimate must still load.
	good := []struct{ name, body string }{
		{"a bare host:port", "addr = \"0.0.0.0:4777\"\n"},
		{"a complete certificate pair", "tls_cert = \"/c.pem\"\ntls_key = \"/k.pem\"\n"},
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
