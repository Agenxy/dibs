package main

import (
	"path/filepath"
	"testing"
)

// Browsers serialise an origin without the scheme's default port, so a
// board served on 80 or 443 saw `http://127.0.0.1` and compared an empty
// port with its own: navigation loaded the page and every authenticated
// action got 403.
func TestTheOriginCheckAcceptsTheBoardsOwnOriginOnADefaultPort(t *testing.T) {
	for _, tc := range []struct {
		addr, origin, reqHost string
		want                  bool
	}{
		{"127.0.0.1:80", "http://127.0.0.1", "127.0.0.1", true},
		{"127.0.0.1:80", "http://127.0.0.1:80", "127.0.0.1", true},
		{"127.0.0.1:443", "https://127.0.0.1", "127.0.0.1", true},
		// Still not any port: a page on another local port is a stranger.
		{"127.0.0.1:80", "http://127.0.0.1:8080", "127.0.0.1", false},
		{"127.0.0.1:443", "http://127.0.0.1", "127.0.0.1", false},
	} {
		t.Run(tc.addr+" <- "+tc.origin, func(t *testing.T) {
			gate := newAuthGate("the-secret", filepath.Join(t.TempDir(), "admin.hash"), tc.addr)
			if got := gate.localOrigin(tc.origin, tc.reqHost); got != tc.want {
				t.Fatalf("a board on %s asked by origin %q: localOrigin = %v, want %v", tc.addr, tc.origin, got, tc.want)
			}
		})
	}
}
