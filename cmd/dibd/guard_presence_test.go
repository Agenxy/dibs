//go:build dibdev

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/humanauth"
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

// The board maps a busy prompt to 409 rather than to a failure.
//
// The serialisation itself lives in internal/humanauth, with the prompt, and is
// tested there against the real Check. What is this package's business is that
// the handler says the right thing: an operator who gets "presence check
// failed" while a sheet is on their screen has been told the machine is broken
// when the answer is "answer the one you already have".
func TestABusyPresencePromptIsAConflictNotAFailure(t *testing.T) {
	if !errors.Is(humanauth.ErrPromptBusy, humanauth.ErrPromptBusy) {
		t.Fatal("the sentinel is not comparable, so the handler cannot distinguish it")
	}
	// The mapping the handler makes, stated where a reader of this package will
	// find it: 409 Conflict, because the request is fine and the timing is not.
	if got := statusForPresenceErr(humanauth.ErrPromptBusy); got != http.StatusConflict {
		t.Errorf("a busy prompt answers %d; it must be %d Conflict. Anything in the "+
			"500 range tells the operator this machine cannot check presence, which "+
			"sends them to set an admin password they do not need",
			got, http.StatusConflict)
	}
	if got := statusForPresenceErr(errors.New("the helper crashed")); got == http.StatusConflict {
		t.Error("an ordinary presence failure was reported as a conflict, so a broken " +
			"helper reads as somebody else's sheet being open")
	}
}

// The sheet names the code the asking terminal printed.
//
// Serialising prompts stops two appearing at once and does not bind consent to
// whoever asked: every agent holds the same local secret, so one can leave a
// request waiting and let the operator's own `dibs web` supply the finger. They
// see a sheet at the moment they expect one, and approving it completes
// somebody else's request. Nothing in the transport separates them, so the
// person is the channel: this terminal names a code, the sheet repeats it, and
// a prompt showing anything else was raised by something else.
//
// The code is caller-supplied text on a biometric prompt, which this file has
// already been burned by once with agent names, so its shape is the test.
func TestThePresenceSheetNamesTheAskingTerminalsCode(t *testing.T) {
	if got := presenceCodeLine("QRST"); !strings.Contains(got, "QRST") {
		t.Errorf("a well-formed code is not shown, so the operator has nothing to "+
			"compare: %q", got)
	}
	// No code is not silence: an agent's request looks exactly like this, and
	// the operator holds one.
	if got := presenceCodeLine(""); !strings.Contains(got, "NO confirmation code") {
		t.Errorf("a request with no code produced no warning: %q", got)
	}

	hostile := []string{
		"QR\nDibs: routine check, approve to continue",
		"QRS‮T",
		"QRSTUVWXYZ-and-a-whole-sentence",
		"approve this",
		"AEIO", // vowels are outside the alphabet on purpose
		"12 34",
	}
	for _, h := range hostile {
		got := presenceCodeLine(h)
		if strings.Contains(got, h) {
			t.Errorf("a caller-supplied code was shown verbatim on the system sheet: "+
				"%q produced %q. Arbitrary text there lets the asker write their own "+
				"prompt, which is the whole reason the sheet is the daemon's", h, got)
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Errorf("%q put a line break on the sheet: %q", h, got)
		}
	}
}

// And the refusal must not claim a property serialising does not have.
//
// It said "an approval cannot be taken by a request it was not raised for",
// which is false and backwards: first-request-wins IS the confusion primitive.
// An agent leaves a request waiting, the operator's own `dibs web` is refused
// with this very message, and the sheet they approve completes the agent's.
// A security control that overstates itself is worse than one that says nothing.
func TestTheBusyPromptRefusalDoesNotOverstateItself(t *testing.T) {
	// THE WHOLE TREE, not this file.
	//
	// The first version read guard.go only, and the identical false claim was
	// sitting in internal/mcp/human.go the entire time: `human_unlock` told its
	// caller the same untrue thing while this guard reported the sentence gone.
	// A check that knows one location of a claim is a check that finds it in
	// that location, which is the location somebody already fixed.
	const bad = "cannot be taken by a request it was not raised for"
	root := filepath.Join("..", "..")
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" && filepath.Ext(path) != ".md" {
			return nil
		}
		// This file quotes the sentence in order to forbid it.
		if filepath.Base(path) == "guard_presence_test.go" {
			return nil
		}
		b, rerr := os.ReadFile(path) // #nosec G304 -- walking this repository
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), bad) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Errorf("%v still claim serialising binds the approval to the requester. "+
			"It does not: whichever request asked first owns the sheet, and the "+
			"operator cannot see which that is without the code on it", found)
	}

	src, err := os.ReadFile("guard.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "DECLINE the sheet on screen") {
		t.Error("the refusal does not tell the operator what to actually do when a " +
			"prompt they did not start is already waiting")
	}
}
