package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/boardconfig"
)

// A dibs.toml with a setting this bridge does not know is still read for the
// settings it does know: an older bridge on a newer daemon's file dialled
// https at a board configured for plaintext.
func TestUnknownSettingsDoNotMakeTheTransportAGuess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	toml := "addr = \"192.168.50.10:4777\"\ninsecure_plaintext = true\n[future]\nsetting = 1\n"
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", "")
	if err := configReadable(dir); err != nil {
		t.Fatal("setup: the shared acceptance refused the file, so there is no compatibility to keep:", err)
	}
	scheme, _, err := resolveTransport(dir)
	var unknown *boardconfig.UnknownSettingsError
	if !errors.As(err, &unknown) {
		t.Fatalf("resolveTransport did not pass the unknown setting up typed (%v): mcp-config "+
			"could no longer refuse a typo by name", err)
	}
	if scheme != "http" {
		t.Errorf("scheme = %q for a board configured insecure_plaintext = true: the unknown "+
			"setting made the transport a guess", scheme)
	}
	if got := origin(); !strings.HasPrefix(got, "http://") {
		t.Errorf("the CLI dials %q at a plaintext board", got)
	}
}
