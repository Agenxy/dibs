package main

// The board link and the one-time grant that opens it.
//
// Split out of main.go, which reached the file-length limit: this is a coherent
// unit rather than an arbitrary cut. Everything here is about turning "the
// operator wants to look at the board" into a single-use URL, including the
// confirmation code that lets them tell their own request from an agent's.

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// errNoPresenceHere is the daemon saying this machine cannot check presence at
// all.
//
// The CLI falls back on every failure, so it no longer branches on this, but the
// distinction is still worth carrying: it is the one presence answer that is not
// about a person, and it is what the daemon's 412 means to anything else that
// learns to call /bootstrap.
var errNoPresenceHere = errors.New("this machine cannot check presence")

type boardGrant struct {
	BT     string `json:"bt"`
	Proof  string `json:"proof"`
	Mocked string `json:"mocked"`
}

// mintBoard asks the daemon for a one-time bootstrap token. The durable secret
// never enters the URL.
func mintBoard(secret, adminPass string, presence bool, code string) (boardGrant, error) {
	var out boardGrant
	req, err := http.NewRequest(http.MethodPost, origin()+"/bootstrap", nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("X-Dibs-Local", secret)
	if presence {
		req.Header.Set("X-Dibs-Presence", "1")
		req.Header.Set("X-Dibs-Presence-Code", code)
	} else {
		req.Header.Set("X-Dibs-Admin", adminPass)
	}
	// No client deadline on the presence path: the person has ninety seconds to
	// reach the sensor and the daemon owns that bound. A shorter one here would
	// cancel the request out from under a sheet they were still looking at.
	resp, err := daemonClient(0).Do(req)
	if err != nil {
		return out, reachErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return out, errNoPresenceHere
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if msg := strings.TrimSpace(string(body)); msg != "" {
			return out, errors.New(msg)
		}
		return out, fmt.Errorf("bootstrap failed: %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, err
	}
	// A 200 THAT CARRIES NOTHING IS NOT A GRANT.
	//
	// Any object that decodes was accepted, so a truncated or unexpected
	// response produced an empty token, printBoardLink emitted `/?bt=` and the
	// command exited zero. The operator opens a link that cannot unlock
	// anything and has no idea why: success reported for a credential that was
	// never issued, which is the shape this release has spent its whole review
	// removing everywhere else.
	if out.BT == "" {
		return out, errors.New("the daemon returned no bootstrap token, so there is " +
			"no link to open. Nothing was unlocked; try again, and if it repeats " +
			"check the daemon's log for what it refused")
	}
	return out, nil
}

func printBoardLink(out boardGrant) error {
	// THE SCHEME THIS BOARD ACTUALLY SERVES, and a host that can be dialled.
	//
	// This hardcoded http:// and printed the raw listen address. The token was
	// minted through origin(), which resolves the transport correctly, and then
	// the link handed the operator a plaintext URL for a board serving HTTPS: a
	// two-minute bearer for a twelve-hour god-view session, in a request any
	// passive observer on the path can read and race to redeem. It also printed
	// 0.0.0.0 for a wildcard bind, which connects from nowhere, and this
	// release made that reachable from an ordinary wizard-written dibs.toml by
	// teaching addr() to read the config.
	//
	// clientHost already exists for the second half and was not called here.
	host, err := clientHost(addr())
	if err != nil {
		return err
	}
	fmt.Printf("%s%s/?bt=%s\n", schemeFor(origin()), host, out.BT)
	if out.Mocked != "" {
		fmt.Fprintln(os.Stderr, "\n# "+out.Mocked)
	}
	how := "the admin password"
	if out.Proof == "presence" {
		how = "your fingerprint"
	}
	fmt.Fprintln(os.Stderr, "\n# Single-use link, expires in 2 minutes, unlocked with "+how+
		". It sets a session cookie; the secret is never in the URL.")
	return nil
}

// presenceCode is the short word this terminal asks the daemon to print on the
// system sheet, so the operator can tell their own request from somebody
// else's.
//
// Short enough to compare at a glance and read out loud, from crypto/rand
// because guessing it is the whole attack. Letters only, and no vowels: a code
// that can spell something is a code somebody argues with, and I and O next to
// 1 and 0 is how a comparison gets waved through.
func presenceCode() (string, error) {
	const alphabet = "BCDFGHJKLMNPQRSTVWXZ"
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("could not generate a confirmation code: %w", err)
	}
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}

// schemeFor extracts the scheme, with its separator, from a resolved origin.
//
// Taken from origin() rather than re-derived, because origin() is what the
// token was minted through: the link and the request that produced it have to
// describe the same board, and inferring twice is how they stopped doing so.
func schemeFor(origin string) string {
	if scheme, _, found := strings.Cut(origin, "://"); found {
		return strings.ToLower(scheme) + "://"
	}
	return schemePlain
}
