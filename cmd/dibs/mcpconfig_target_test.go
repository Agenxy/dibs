package main

import (
	"strings"
	"testing"
)

// A wildcard bind is not a destination a client can dial.
//
// `DIBS_ADDR` was copied verbatim into every generated client configuration,
// because the scheme it may carry is the one thing that cannot be inferred. A
// daemon legitimately started with `DIBS_ADDR=:4777` or `0.0.0.0:4777` therefore
// published its BIND address as the client's dial target, and `:4777` has no
// host in it at all. The configuration branch beside it has always resolved
// this; the environment branch did not, and the existing wildcard coverage
// clears DIBS_ADDR, so it could not reach the case.
func TestAWildcardEnvironmentAddressIsResolvedForClients(t *testing.T) {
	for _, tc := range []struct{ set, wantNot string }{
		{":4777", ":4777"},
		{"0.0.0.0:4777", "0.0.0.0:4777"},
		{"http://0.0.0.0:4777", "http://0.0.0.0:4777"},
	} {
		t.Run(tc.set, func(t *testing.T) {
			t.Setenv("DIBS_ADDR", tc.set)
			got, err := dialableAddr(tc.set)
			if err != nil {
				t.Skipf("this machine cannot name itself (%v), so the resolution "+
					"cannot be checked here", err)
			}
			if got == tc.wantNot {
				t.Errorf("a client is told to dial %q, which is where the daemon "+
					"LISTENS. A wildcard answers on every interface and names none of "+
					"them, and `:4777` has no host at all", got)
			}
			// The scheme, which is why the raw value was being passed through,
			// must survive the resolution.
			if strings.HasPrefix(tc.set, "http://") && !strings.HasPrefix(got, "http://") {
				t.Errorf("resolving %q dropped the scheme and gave %q: a plaintext "+
					"daemon off loopback is then dialled over TLS", tc.set, got)
			}
		})
	}
}
