package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/supgang"
)

// The test binary doubles as a stand-in `supgang` that answers as the real
// one did on 2026-09-13, so the join-by-peer path is exercised against real
// envelopes.
func TestMain(m *testing.M) {
	if os.Getenv("DIBS_TEST_AS_SUPGANG") != "" {
		os.Exit(fakeSupgang(os.Args[1:]))
	}
	// No test here reaches the real supgang on this machine's PATH: a
	// developer's hive would otherwise answer for the fleet a test set up.
	supgang.Command = "/nonexistent/supgang-under-test"
	os.Exit(m.Run())
}

func fakeSupgang(args []string) int {
	if len(args) < 2 || args[0] != "--json" {
		return 2
	}
	switch args[1] {
	case "status":
		_, _ = os.Stdout.WriteString(`{"schema":"supgang.status/v4","status":"ok","version":"0.2.0-alpha.10","name":"MacMarine","hive_id":"5cb4e356","node_id":"fed08b444ee029ef43b8c04106199408f449d6857f60c16423d32f8dbbe77621","service":"running","mode":"device"}`)
	case "resolve":
		if len(args) < 3 || (args[2] != "MacSolis" && args[2] != "solis") {
			_, _ = os.Stdout.WriteString(`{"schema":"supgang.error/v1","status":"error","error":"no known peer significantly matches that name, tag, or fingerprint"}`)
			return 3
		}
		_, _ = os.Stdout.WriteString(`{"schema":"supgang.resolve/v4","status":"ok","node_id":"a8a37e32c37cdf4fb7634de622bc3f84ccb4636580d1ea26af3fe8ac31d1f152","name":"MacSolis","tags":["solis"],"fingerprint":"a8a37e32","candidates":[{"scope":"local","kind":"local","transport":"quic-v1","address":"192.168.1.191:44330","provenance":"device-signed","route_compatible":true,"preferred":true}]}`)
	default:
		return 2
	}
	return 0
}

func useFakeSupgang(t *testing.T) {
	t.Helper()
	old := supgang.Command
	supgang.Command = os.Args[0]
	t.Setenv("DIBS_TEST_AS_SUPGANG", "1")
	t.Cleanup(func() { supgang.Command = old })
}

// `dibs mcp-config --board MacSolis` joins the hub the way the fleet names
// it: Supgang's signed address for that computer, Dibs's own port, https,
// and the peer recorded so the bridge can ask again later. An address is
// still an address, and a name Supgang does not know is not turned into one.
func TestABoardCanBeNamedAsASupgangPeer(t *testing.T) {
	useFakeSupgang(t)
	remote, peer, err := resolveBoardPeer("MacSolis")
	if err != nil || remote != "https://192.168.1.191:4777" || peer == nil || peer.Fingerprint != "a8a37e32" {
		t.Fatalf("resolveBoardPeer(MacSolis) = %q %+v %v", remote, peer, err)
	}
	if remote, _, err := resolveBoardPeer("solis:4790"); err != nil || remote != "https://192.168.1.191:4790" {
		t.Errorf("resolveBoardPeer(solis:4790) = %q %v: the port is Dibs's, not Supgang's", remote, err)
	}
	for _, bad := range []string{"solis:0", "solis:65536", "solis:-1", "solis:x"} {
		if _, _, err := resolveBoardPeer(bad); err == nil || errors.Is(err, errNotAPeer) {
			t.Errorf("resolveBoardPeer(%q) = %v, want a refusal naming the port", bad, err)
		}
	}
	// Two boards on one peer are two secrets: their directories differ.
	t.Setenv("HOME", t.TempDir())
	dirs := map[string]bool{}
	for _, b := range []string{"MacSolis", "MacSolis:4790"} {
		remote, peer, err := resolveBoardPeer(b)
		if err != nil {
			t.Fatal(err)
		}
		dirs[joinDirFor(remote, peer)] = true
	}
	if len(dirs) != 2 {
		t.Errorf("two boards on one peer share a credential directory: %v", dirs)
	}
	for _, addr := range []string{"192.168.1.5:4777", "https://hub:4777", "[::1]:4777", "nobody", "nobody:4777"} {
		if _, _, err := resolveBoardPeer(addr); !errors.Is(err, errNotAPeer) {
			t.Errorf("resolveBoardPeer(%q) = %v, want errNotAPeer so the address path takes over", addr, err)
		}
	}
}

// The bridge dials where Supgang says the hub is NOW, keeping Dibs's scheme
// and port; without a peer, or when Supgang cannot say, the configured
// address is dialled as given.
func TestTheBridgeDialsTheHubWhereSupgangSaysItIsNow(t *testing.T) {
	useFakeSupgang(t)
	t.Setenv("DIBS_ADDR", "https://10.0.0.9:4790")
	t.Setenv(boardPeerEnv, "")
	if got := boardOrigin(); got != "https://10.0.0.9:4790" {
		t.Errorf("without a peer, boardOrigin = %q", got)
	}
	t.Setenv(boardPeerEnv, "MacSolis")
	if got := boardOrigin(); got != "https://192.168.1.191:4790" {
		t.Errorf("with a peer, boardOrigin = %q, want Supgang's host with Dibs's scheme and port", got)
	}
	t.Setenv(boardPeerEnv, "nobody")
	if got := boardOrigin(); got != "https://10.0.0.9:4790" {
		t.Errorf("with a peer Supgang does not know, boardOrigin = %q, want the configured address", got)
	}
	if !strings.HasPrefix(boardOrigin(), "https://") {
		t.Error("the scheme was lost")
	}
}
