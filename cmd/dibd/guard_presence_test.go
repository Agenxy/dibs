//go:build dibdev

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// These need a scripted presence verdict, so they live behind the dibdev tag
// with the mock. The release build cannot be told a human is present, which is
// asserted separately and by design in internal/humanauth.

func gateWithNoPassword(t *testing.T) *authGate {
	t.Helper()
	// A path that does not exist: adminHash() returns "", which is the state a
	// machine is in before anybody runs `dibs admin set-password`, and the whole
	// point of what follows.
	return newAuthGate("the-secret", filepath.Join(t.TempDir(), "admin.hash"), "127.0.0.1:4777")
}

func bootstrapReq(presence bool, secret string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/bootstrap", nil)
	if secret != "" {
		r.Header.Set("X-Dibs-Local", secret)
	}
	if presence {
		r.Header.Set("X-Dibs-Presence", "1")
	}
	return r
}

// A fingerprint opens the board on a machine with no admin password.
//
// This is the whole point. `dibs doctor` used to warn "no admin password set,
// so the web board cannot be opened" on every Mac with a working sensor, and
// the remedy it named was to invent and store a credential in order to be
// trusted LESS: internal/mcp/human.go has said since it was written that a
// password proves possession of a secret an agent could have been handed, while
// a fingerprint proves somebody is sitting there. The panel took the stronger
// proof and this gate demanded the weaker one.
func TestAFingerprintOpensTheBoardWithNoPasswordSet(t *testing.T) {
	t.Setenv("DIBS_PRESENCE_MOCK", "verified")
	g := gateWithNoPassword(t)

	w := httptest.NewRecorder()
	g.wrap(http.NotFoundHandler()).ServeHTTP(w, bootstrapReq(true, "the-secret"))

	if w.Code != http.StatusOK {
		t.Fatalf("presence bootstrap returned %d, want 200: %s", w.Code, w.Body.String())
	}
	var out struct{ BT, Proof, Mocked string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.BT == "" {
		t.Error("no bootstrap token was minted, so there is nothing to open the board with")
	}
	if out.Proof != "presence" {
		t.Errorf("proof = %q, want %q: the caller prints which credential unlocked it, "+
			"and a person told they used a password they never typed is being lied to",
			out.Proof, "presence")
	}
	// A scripted verdict must never be indistinguishable from a real one in the
	// artefact somebody would cite as evidence the feature works.
	if out.Mocked == "" {
		t.Error("a mocked verdict was not declared in the response: a dev build that " +
			"answers presence from an environment variable has to say so, or the " +
			"transcript proving 'Touch ID works' proves nothing")
	}

	// And the token really is a session: minting one that does not redeem would
	// pass every check above and open nothing.
	if _, ok := g.redeemBootstrap(out.BT); !ok {
		t.Error("the minted token did not redeem into a god-view session")
	}
}

// Presence is the SECOND factor, never the only one.
//
// The two tiers are same-user (the local secret, which every agent on this
// machine legitimately holds) and human (this). Accepting a fingerprint without
// the secret would turn the board into something any process on the box could
// open by waiting for the operator to touch the sensor for an unrelated reason.
func TestPresenceDoesNotReplaceTheLocalSecret(t *testing.T) {
	t.Setenv("DIBS_PRESENCE_MOCK", "verified")
	g := gateWithNoPassword(t)

	w := httptest.NewRecorder()
	g.wrap(http.NotFoundHandler()).ServeHTTP(w, bootstrapReq(true, ""))

	if w.Code == http.StatusOK {
		t.Error("a verified fingerprint alone minted a board session: presence is the " +
			"second factor, and the first is still same-user")
	}
}

// Declined and Unavailable are different answers, because the remedies are
// opposite: one is a person saying no, and the other is a machine that has
// nothing to ask with.
//
// Collapsing them is not cosmetic. `dibs web` falls back to the password on
// Unavailable and stops on Declined; if this gate answered a sensorless Linux
// box with "declined" the CLI would report a refusal nobody made and never
// offer the password. If it answered a declined fingerprint with "unavailable"
// the CLI would immediately demand a password from somebody who had just said
// no, which is the shape of a prompt that trains people to type credentials.
func TestDeclinedAndUnavailableAreDistinguished(t *testing.T) {
	for _, c := range []struct {
		mock string
		want int
		why  string
	}{
		{
			"declined", http.StatusUnauthorized,
			"a person said no; the caller must stop rather than ask for a password",
		},
		{
			"unavailable", http.StatusPreconditionFailed,
			"nobody was asked; the caller must fall back to the password",
		},
	} {
		t.Run(c.mock, func(t *testing.T) {
			t.Setenv("DIBS_PRESENCE_MOCK", c.mock)
			g := gateWithNoPassword(t)

			w := httptest.NewRecorder()
			g.wrap(http.NotFoundHandler()).ServeHTTP(w, bootstrapReq(true, "the-secret"))

			if w.Code != c.want {
				t.Errorf("%s returned %d, want %d: %s", c.mock, w.Code, c.want, c.why)
			}
			if w.Code == http.StatusOK {
				t.Error("a session was minted without a verified human")
			}
		})
	}
}
