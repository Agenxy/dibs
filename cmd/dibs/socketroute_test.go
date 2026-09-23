package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A DIAGNOSTIC THAT KEEPS DEMANDING A FIX AFTER THE FIX IS IN PLACE TRAINS AN
// OPERATOR TO SKIM IT.
//
// The socket wake route is held by default for a session in bypassPermissions
// mode, which is what an unattended fleet runs. `dibs doctor` said so and named
// only the expensive remedy, and the cheap one is a single line in the
// receiving user's settings. Naming that line created the other half of the
// problem: advice printed unconditionally is advice printed to somebody who has
// already taken it, and the next real warning then reads like more of the same.
//
// So the check reads the setting. These are the four states it has to tell
// apart, each with the thing an operator can act on.
func TestTheSocketRouteCheckReadsTheSettingItAdvisesAbout(t *testing.T) {
	write := func(t *testing.T, value string) string {
		t.Helper()
		home := t.TempDir()
		if value != "" {
			dir := filepath.Join(home, ".claude")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			body := `{"crossSessionInbound":"` + value + `"}`
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return home
	}
	// report runs the check against a home with that setting and returns
	// everything it said, ticks and warnings alike.
	report := func(t *testing.T, value string) (ticks, warns, fixes []string) {
		t.Helper()
		t.Setenv("HOME", write(t, value))
		reportSocketRoute(t.TempDir(),
			func(s string) { ticks = append(ticks, s) },
			func(w, f string) { warns = append(warns, w); fixes = append(fixes, f) })
		return ticks, warns, fixes
	}

	t.Run("unset is the default, and both ways out are offered", func(t *testing.T) {
		ticks, warns, fixes := report(t, "")
		if len(ticks) != 0 {
			t.Errorf("the held default was reported as healthy: %v", ticks)
		}
		if len(warns) != 1 {
			t.Fatalf("want one warning, got %v", warns)
		}
		all := strings.Join(fixes, " ")
		if !strings.Contains(all, "crossSessionInbound") {
			t.Error("the cheap remedy is not named, which is the omission this check exists for")
		}
		if !strings.Contains(all, "wake.exec") {
			t.Error("the confirmable route is not named")
		}
	})

	t.Run("accept is reported as open and stops asking for the setting", func(t *testing.T) {
		ticks, warns, fixes := report(t, "accept")
		if len(ticks) != 1 || !strings.Contains(ticks[0], "open") {
			t.Fatalf("an open route was not reported as open: %v", ticks)
		}
		// Still a warning, because open is not confirmable: no receipt comes
		// back on this route whatever the setting says.
		if len(warns) != 1 || !strings.Contains(warns[0], "confirm") {
			t.Errorf("an unconfirmable route was not said to be unconfirmable: %v", warns)
		}
		// AND THE POINT OF THE WHOLE CHECK. An operator who has set it must not
		// be told to set it.
		for _, f := range fixes {
			if strings.Contains(f, `"crossSessionInbound": "accept"`) {
				t.Errorf("told to apply a fix that is already applied:\n%s", f)
			}
		}
	})

	t.Run("hold names the file that is holding", func(t *testing.T) {
		ticks, warns, fixes := report(t, "hold")
		if len(ticks) != 0 {
			t.Errorf("a held route was reported as healthy: %v", ticks)
		}
		if len(warns) != 1 || !strings.Contains(warns[0], "held") {
			t.Fatalf("want a hold warning, got %v", warns)
		}
		if !strings.Contains(fixes[0], "your own settings") {
			t.Errorf("the deciding file is not named, so there is nowhere to go:\n%s", fixes[0])
		}
	})

	t.Run("refuse says no local change will help", func(t *testing.T) {
		_, warns, fixes := report(t, "refuse")
		if len(warns) != 1 || !strings.Contains(warns[0], "refused") {
			t.Fatalf("want a refuse warning, got %v", warns)
		}
		// Refuse is not a state that "set accept" answers: the operator has
		// opted that side out, so the honest advice is the other route.
		if !strings.Contains(fixes[0], "wake.exec") {
			t.Errorf("a refused route is not pointed at the route that still works:\n%s", fixes[0])
		}
	})
}
