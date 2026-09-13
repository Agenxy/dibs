package main

import "testing"

// Remap's target and the daemon's own origin describe one daemon when host
// and port agree; the scheme is the gateway's business and a trailing slash
// is nobody's.
func TestABoardNameIsRoutedWhenTheTargetIsThisDaemon(t *testing.T) {
	for mapped, same := range map[string]bool{
		"http://127.0.0.1:4777/":   true,
		"HTTP://127.0.0.1:4777":    true,
		"127.0.0.1:4777":           true,
		"https://127.0.0.1:4777/":  false, // TLS to a plaintext listener reaches nothing
		"http://127.0.0.1:4778/":   false,
		"http://192.168.1.5:4777/": false,
		"http://localhost:4777/":   false, // a different spelling is a different upstream to Remap
	} {
		if got := sameBoardTarget(mapped, "http://127.0.0.1:4777/"); got != same {
			t.Errorf("sameBoardTarget(%q) = %v, want %v", mapped, got, same)
		}
	}
}
