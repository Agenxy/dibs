package notify

import (
	"os"
	"path/filepath"
	"runtime"
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
	for _, tc := range []struct{ name, body, want string }{
		{"measured version", `{"header":{"version":3},"data":[{"modeConfigurations":{"com.apple.focus.fixture":{"mode":{"name":"Personal"}}}}]}`, "Personal"},
		{"unknown version", `{"header":{"version":4},"data":[{"modeConfigurations":{"com.apple.focus.fixture":{"mode":{"name":"Unverified"}}}}]}`, "fixture"},
		{"wrong name type", `{"header":{"version":3},"data":[{"modeConfigurations":{"com.apple.focus.fixture":{"mode":{"name":4}}}}]}`, "fixture"},
		{"malformed", `{`, "fixture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(db, "ModeConfigurations.json"), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			p := FocusPresentation()
			if p.Focus != tc.want || p.Shown != "unknown" || p.Reason == "" {
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
