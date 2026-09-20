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

// A name is offered as a link only when a session can travel through it.
//
// Remap serves `http://<name>/` and proxies to the target; the daemon's
// session cookie is Secure when its own leg is TLS, and a browser at an
// http:// origin never keeps a Secure cookie. Printing the name for an
// HTTPS board handed the operator a link that consumed the single-use
// token and unlocked nothing. Round seventeen of the pre-release review.
func TestANamedLinkIsPrintedOnlyForABoardRemapCanCarryASessionTo(t *testing.T) {
	if link, note := namedLink("board", "http://127.0.0.1:4777", "tok"); link != "http://board/?bt=tok" || note != "" {
		t.Fatalf("plain HTTP: link %q note %q, want the named link and no note", link, note)
	}
	link, note := namedLink("board", "https://192.168.1.5:4777", "tok")
	if link != "" {
		t.Fatalf("an HTTPS board got the named link %q: redeeming it sets a Secure cookie an "+
			"http:// origin discards, so the one link that could unlock the board is spent on "+
			"nothing", link)
	}
	if note == "" {
		t.Fatal("the name was withheld silently: the operator has a name in dibs.toml and " +
			"deserves to hear why the link does not carry it")
	}
}
