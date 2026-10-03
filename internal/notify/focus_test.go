package notify

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFocusObservationNeverInfersVisibility(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS observation files")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	db := filepath.Join(dir, "Library", "DoNotDisturb", "DB")
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}
	assert := filepath.Join(db, "Assertions.json")
	if err := os.WriteFile(assert, []byte(`{"data":[{"storeAssertionRecords":[{"assertionDetails":{"assertionDetailsModeIdentifier":"com.apple.focus.fixture"}}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, body, want, advice string }{
		{"measured version", `{"header":{"version":3},"data":[{"modeConfigurations":{"com.apple.focus.fixture":{"mode":{"name":"Personal"}}}}]}`, "Personal", "Check Personal"},
		{"allow list", `{"header":{"version":3},"data":[{"modeConfigurations":{"com.apple.focus.fixture":{"mode":{"name":"Personal"},"configuration":{"applicationConfigurationType":0}}}}]}`, "Personal", "add Dibs to Personal's Allowed Apps"},
		{"silence list", `{"header":{"version":3},"data":[{"modeConfigurations":{"com.apple.focus.fixture":{"mode":{"name":"Personal"},"configuration":{"applicationConfigurationType":1}}}}]}`, "Personal", "Make sure Dibs isn't in Personal's silenced apps"},
		{"unknown list", `{"header":{"version":3},"data":[{"modeConfigurations":{"com.apple.focus.fixture":{"mode":{"name":"Personal"},"configuration":{"applicationConfigurationType":2}}}}]}`, "Personal", "Check Personal"},
		{"unknown version", `{"header":{"version":4},"data":[{"modeConfigurations":{"com.apple.focus.fixture":{"mode":{"name":"Unverified"}}}}]}`, "fixture", "Check fixture"},
		{"wrong name type", `{"header":{"version":3},"data":[{"modeConfigurations":{"com.apple.focus.fixture":{"mode":{"name":4}}}}]}`, "fixture", "Check fixture"},
		{"malformed", `{`, "fixture", "Check fixture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(db, "ModeConfigurations.json"), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			doctor := focusDoctor()
			if !strings.Contains(doctor, tc.advice) || strings.Contains(doctor, "this") || strings.Contains(doctor, "not confirmed seen") {
				t.Fatalf("doctor advice: %s", doctor)
			}
			p := FocusPresentation()
			if p.Focus != tc.want || p.Shown != "unknown" || !strings.Contains(p.Reason, "not confirmed seen") || !strings.Contains(p.Reason, tc.advice) {
				t.Fatalf("observation: %+v", p)
			}
		})
	}
	if err := os.WriteFile(assert, []byte(`{"data":[{"storeAssertionRecords":[]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p := FocusPresentation(); p.Focus != "" || p.Shown != "" || p.Reason != "" {
		t.Fatalf("inactive Focus caveat: %+v", p)
	}
	if err := os.WriteFile(assert, make([]byte, (1<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFocusFile(assert); err == nil {
		t.Fatal("unbounded Focus file accepted")
	}
}
