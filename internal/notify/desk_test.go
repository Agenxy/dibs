package notify

import "testing"

func TestAwayRequiresKnownLockOrSleepingDisplays(t *testing.T) {
	for name, desk := range map[string]Desk{
		"unknown":         {},
		"untrusted lock":  {Locked: true},
		"untrusted sleep": {DisplaysAsleep: true},
		"awake":           {SessionKnown: true, DisplayKnown: true},
	} {
		if desk.Away() {
			t.Errorf("%s authorized a visible open", name)
		}
	}
	for name, desk := range map[string]Desk{
		"known locked":   {SessionKnown: true, Locked: true},
		"known sleeping": {DisplayKnown: true, DisplaysAsleep: true},
	} {
		if !desk.Away() {
			t.Errorf("%s did not authorize away delivery", name)
		}
	}
}
