package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Upgrade reads the board of the daemon it is upgrading.
//
// The plan discovers the target's real address from the registry each live
// daemon writes, and it does that on purpose: assuming the address is how a
// board serving on a LAN address gets restarted on loopback, taking every
// remote agent off it while every local check still passes. Having found the
// address, both the before-snapshot and the verification then asked the
// address-free helper, which resolves through this CLI's own environment and
// config. So the proof that "the board came back" came from whichever daemon
// THAT named.
//
// With two boards up, the pre-release review stopped one, read the other, and
// watched upgrade print "upgraded: serial 0, 0 agent(s)" and return success
// while its target served nothing. With one board on an address the CLI does
// not know, the restart works and a failure is reported that did not happen.
// Discovering an address and then not using it is worse than never discovering
// it, because the report is confident either way.
func TestUpgradeReadsTheBoardItIsUpgrading(t *testing.T) {
	board := func(serial int, agents int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rows := make([]string, agents)
			for i := range rows {
				rows[i] = `{"id":"a"}`
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"serial":%d,"node":"n","agents":[%s]}`,
				serial, strings.Join(rows, ","))
		}))
	}
	target := board(42, 2) // the daemon being upgraded
	defer target.Close()
	other := board(7, 0) // a different board, which the CLI happens to point at
	defer other.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"),
		[]byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", strings.TrimPrefix(other.URL, "http://"))

	got, err := fleetSnapshotAt(strings.TrimPrefix(target.URL, "http://"))
	if err != nil {
		t.Fatalf("reading the target board: %v", err)
	}
	if got.Serial != 42 || got.Agents != 2 {
		t.Errorf("read serial %d with %d agent(s): that is the OTHER board. "+
			"Upgrade would stop one daemon and report the health of another",
			got.Serial, got.Agents)
	}

	// And with nothing discovered, this CLI's own target is still the answer:
	// a fix that ignored the environment entirely would break every ordinary
	// invocation, where the registry has no address to offer.
	fallback, err := fleetSnapshot()
	if err != nil {
		t.Fatalf("reading the configured board: %v", err)
	}
	if fallback.Serial != 7 {
		t.Errorf("with no address discovered, serial %d: the CLI's configured "+
			"target must still be used", fallback.Serial)
	}
}
