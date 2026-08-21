package transport

import "testing"

// One rule, and these are the cases it was got wrong on.
//
// The daemon decided from its configuration and its address; `dibs mcp-config`,
// printing the configuration agents connect with, decided again and disagreed
// three times in three review rounds. Each row below is one of those
// disagreements, and each of them told an operator to configure a client for a
// transport the daemon does not serve.
func TestTheRuleTwoProgramsHaveToAgreeOn(t *testing.T) {
	cases := []struct {
		name              string
		cert, key, addr   string
		insecurePlaintext bool
		wantTLS           bool
	}{
		{
			"loopback is plaintext however many certificates are lying around",
			"", "", "127.0.0.1:4777", false, false,
		},
		{
			"a certificate PAIR beats insecure_plaintext, as the daemon does",
			"/c.pem", "/k.pem", "192.168.1.5:4777", true, true,
		},
		{
			"insecure_plaintext off loopback is the operator accepting it",
			"", "", "192.168.1.5:4777", true, false,
		},
		{
			"off loopback with nothing said is TLS",
			"", "", "192.168.1.5:4777", false, true,
		},
		{
			"a wildcard bind is reachable, so it is not loopback",
			"", "", "0.0.0.0:4777", false, true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Resolve(c.cert, c.key, c.addr, c.insecurePlaintext,
				func() (string, string, error) { return "/auto.pem", "/auto-key.pem", nil })
			if err != nil {
				t.Fatal(err)
			}
			if got.TLS() != c.wantTLS {
				t.Errorf("TLS = %v, want %v (%s)", got.TLS(), c.wantTLS, got.Why)
			}
		})
	}

	// HALF a pair is refused outright now, rather than falling through to a
	// self-signed certificate the operator did not ask for. Starting on a
	// transport other than the configured one is worse than not starting: the
	// explicit setting did nothing and nothing said so.
	for _, half := range []struct{ cert, key string }{{"/c.pem", ""}, {"", "/k.pem"}} {
		if _, err := Resolve(half.cert, half.key, "192.168.1.5:4777", false,
			func() (string, string, error) { return "/auto.pem", "/auto-key.pem", nil }); err == nil {
			t.Errorf("half a certificate pair (cert=%q key=%q) was accepted, so the "+
				"daemon starts on a transport the operator did not configure",
				half.cert, half.key)
		}
	}
}
