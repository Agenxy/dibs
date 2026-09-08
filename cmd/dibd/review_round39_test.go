package main

import (
	"testing"

	"github.com/agenxy/dibs/internal/paths"
)

// The registry entry a daemon claims carries the transport it was asked
// for, so `dibs upgrade` can restart it as the daemon it was. See
// paths.Daemon.Scheme.
func TestTheClaimRecordsTheSchemeTheDaemonWasAskedFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the registry lives under HOME
	dir := t.TempDir()
	release, err := claimHostSlot("127.0.0.1:1", "https", dir, true)
	if err != nil {
		t.Fatal("setup:", err)
	}
	defer release()
	live, err := paths.LiveDaemons()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range live {
		if paths.Canonical(d.Dir) == paths.Canonical(dir) {
			if d.Scheme != "https" {
				t.Fatalf("the claim registered scheme %q, want https: the upgrade will restart this "+
					"daemon with a bare address", d.Scheme)
			}
			return
		}
	}
	t.Fatal("setup: the claimed daemon is not in the registry")
}
