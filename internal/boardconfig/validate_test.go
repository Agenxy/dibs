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
