package notify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The public API resolves a real helper beside a copied driver. The helper
// refuses --ask, so the old Focus escalation fails without drawing a window.
func TestFocusQuestionUsesBannerThroughInstalledHelper(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS Focus")
	}
	if os.Getenv("DIBS_TEST_FOCUS_DRIVER") == "1" {
		state := ""
		choice, err := AskWithReceipt("fixture", "fixture", func(s string) { state = s }, "Yes")
		if err != nil || choice != "Yes" || state != "posted" {
			t.Fatalf("Focus banner: choice=%q receipt=%q err=%v", choice, state, err)
		}
		return
	}
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	driver, helper := filepath.Join(dir, "driver.test"), filepath.Join(dir, helperName)
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{driver, helper} {
		if err := os.WriteFile(p, b, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	db := filepath.Join(dir, "Library", "DoNotDisturb", "DB")
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(db, "Assertions.json"), []byte(`{"data":[{"storeAssertionRecords":[{"assertionDetails":{"assertionDetailsModeIdentifier":"com.apple.focus.fixture"}}]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, driver, "-test.run=^TestFocusQuestionUsesBannerThroughInstalledHelper$") // #nosec G204 -- private test binary
	cmd.Env = append(os.Environ(), "HOME="+dir, "DIBS_DIR="+dir, "DIBS_TEST_RECEIPT_PUBLIC=1", "DIBS_TEST_FOCUS_DRIVER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("public Focus route: %v\n%s", err, out)
	}
}
