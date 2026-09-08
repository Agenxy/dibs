package boardconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An unknown key is reported as a typed error, and the file still decodes.
//
// Two readers of one file need opposite answers to an unknown key. The daemon
// owns the file and must refuse it, naming the key, or an operator who typed
// `[limit]` for `[limits]` debugs behaviour they believe they configured. The
// bridge only needs the address, and is routinely OLDER than the daemon: every
// `task install` leaves each running session's bridge on the previous build
// until that session restarts. A string error made those two cases identical,
// and a bridge refused a `fallback` key the daemon had already started on,
// claiming the daemon would refuse it too. It was serving at the time.
func TestAnUnknownKeyIsATypedErrorAndTheFileStillDecodes(t *testing.T) {
	dir := t.TempDir()
	toml := "[wake.exec.x]\nargv = [\"a\"]\nfuture_key_this_build_lacks = 1\n"
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err == nil {
		t.Fatal("an unknown key was accepted silently. The daemon must refuse it: " +
			"a misspelled table parses cleanly and leaves the operator certain they " +
			"configured something")
	}
	var unknown *UnknownSettingsError
	if !errors.As(err, &unknown) {
		t.Fatalf("the error is not typed (%T: %v), so a caller cannot tell a key it "+
			"does not know from a file that does not parse, and refuses both", err, err)
	}
	if !strings.Contains(err.Error(), "future_key_this_build_lacks") {
		t.Errorf("the error does not name the key: %v", err)
	}
	// The address and everything else this build DOES know came out of it.
	if got := c.Wake.Exec["x"].Argv; len(got) != 1 || got[0] != "a" {
		t.Errorf("the decoded config was discarded along with the error (%v): a "+
			"reader that only needs the known fields now has nothing", got)
	}
}

// A file that does not parse is not that error. Nothing decoded; the daemon
// really cannot start; a reader must not proceed on a guess.
func TestAMalformedFileIsNotAnUnknownKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[wake\nthis is not toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	var unknown *UnknownSettingsError
	if err == nil || errors.As(err, &unknown) {
		t.Fatalf("a file that does not parse was reported as merely having unknown "+
			"keys (%v), which a lenient reader would proceed on", err)
	}
}
