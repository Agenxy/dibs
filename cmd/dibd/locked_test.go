package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The locked board must not send a Touch ID user off to make a password.
//
// This is the sentence somebody reads at the exact moment they are locked out,
// which makes it the most consequential onboarding surface there is. It said the
// board "needs a session from `dibs web` (admin password)", and the HTML said
// "Opening it takes your admin password". `dibs web` raises the daemon-owned
// Touch ID sheet first and falls back to a password only where there is no
// sensor, so the release announced that it had removed the need for a weaker
// credential while its own lockout page told people to create one. The README
// and the Homebrew caveat said the same and were corrected a round earlier;
// nothing was looking at this one.
//
// BOTH REPRESENTATIONS, because a browser and a script get different bodies and
// only one of them was ever read by a person during the fix.
func TestTheLockedBoardDoesNotDemandAnAdminPassword(t *testing.T) {
	g := &authGate{}
	for _, tc := range []struct{ name, accept string }{
		{"a browser", "text/html"},
		{"a script", "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Accept", tc.accept)
			g.unauthorized(w, r)

			body := strings.ToLower(w.Body.String())
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401: this is not the locked response", w.Code)
			}
			if body == "" {
				t.Fatal("the locked response says nothing, so this check verified nothing")
			}
			if !strings.Contains(body, "password") {
				return // says nothing about a password; nothing to contradict
			}
			for _, ok := range []string{"touch id", "fingerprint", "sensor"} {
				if strings.Contains(body, ok) {
					return
				}
			}
			t.Errorf("the locked board names a password as the way in and never mentions "+
				"the sensor, so a Mac user reading it goes and creates the credential "+
				"`dibs web` no longer asks them for:\n%s", w.Body.String())
		})
	}
}
