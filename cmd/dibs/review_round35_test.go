package main

import (
	"strings"
	"testing"
)

// The tunnel paragraph described every loopback daemon as plaintext and told
// the joiner to put a bare 127.0.0.1:<local-port> in DIBS_ADDR, after the
// block above it had handed over an https:// address for a loopback daemon
// with a certificate pair. A bare address makes the bridge infer plaintext,
// and the trust step that follows cannot change the transport it infers.
func TestTheTunnelRecipeNamesTheTransportTheDaemonServes(t *testing.T) {
	t.Setenv("DIBS_ADDR", "127.0.0.1:4777")
	joiner, err := joinerAddr("https")
	if err != nil {
		t.Fatal("setup:", err)
	}
	if !strings.HasPrefix(joiner, "https://") {
		t.Fatalf("setup: the joiner for a TLS loopback daemon is %q", joiner)
	}
	out, err := captureStdout(t, func() error { printRemoteRecipe(true, joiner); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "plaintext on loopback") {
		t.Errorf("a loopback daemon with a certificate pair is described as plaintext:\n%s", out)
	}
	if !strings.Contains(out, "DIBS_ADDR as "+joiner) {
		t.Errorf("the tunnel paragraph does not hand the joiner the https:// address the block "+
			"above it did; a bare address makes the bridge infer plaintext:\n%s", out)
	}
	// And the ordinary daemon is still described as what it is.
	plain, err := joinerAddr("http")
	if err != nil {
		t.Fatal("setup:", err)
	}
	out, err = captureStdout(t, func() error { printRemoteRecipe(false, plain); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "plaintext on loopback") || !strings.Contains(out, "DIBS_ADDR as "+plain) {
		t.Errorf("the plaintext loopback daemon lost its own description:\n%s", out)
	}
}
