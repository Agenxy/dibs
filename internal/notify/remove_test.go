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

func TestMessageIDsAreScopedAndValidated(t *testing.T) {
	a, err := MessageID("board-A", 7)
	if err != nil {
		t.Fatal(err)
	}
	b, err := MessageID("board-B", 7)
	if err != nil || a == b {
		t.Fatalf("board collision: %q %q %v", a, b, err)
	}
	for _, node := range []string{"", "board.A", "board/A", "board,A", "board\nA"} {
		if _, err := MessageID(node, 7); err == nil {
			t.Fatalf("accepted injected node %q", node)
		}
	}
	if _, err := MessageID("board-A", 0); err == nil {
		t.Fatal("accepted zero serial")
	}
	if result := RemoveMessages("board-A", make([]uint64, CleanupBatch+1)); result.State != "failed" {
		t.Fatalf("unbounded batch: %+v", result)
	}
}

func TestPublicMessageNotificationAndCleanupUseInstalledHelper(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS native-helper lookup")
	}
	if os.Getenv("DIBS_TEST_CLEANUP_DRIVER") == "1" {
		state := ""
		choice, err := AskMessage("board-A", 7, "fixture", "fixture", func(s string) { state = s }, "Yes")
		if err != nil || choice != "Yes" || state != "posted" {
			t.Fatalf("keyed posting: %q %q %v", choice, state, err)
		}
		for _, mode := range []string{"new", "old", "crash"} {
			t.Setenv("DIBS_TEST_CLEANUP_MODE", mode)
			result := RemoveMessages("board-A", []uint64{7, 8})
			want := map[string]string{"new": "requested", "old": "unsupported", "crash": "failed"}[mode]
			if result.State != want || !result.BestEffort {
				t.Fatalf("%s cleanup: %+v", mode, result)
			}
		}
		return
	}
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	driver, helper := filepath.Join(dir, "driver.test"), filepath.Join(dir, helperName)
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{driver, helper} {
		if err := os.WriteFile(path, bytes, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, driver, "-test.run=^TestPublicMessageNotificationAndCleanupUseInstalledHelper$") // #nosec G204 -- private test binary
	cmd.Env = append(os.Environ(), "DIBS_TEST_RECEIPT_PUBLIC=1", "DIBS_TEST_CLEANUP_DRIVER=1", "DIBS_DIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("public notification cleanup wiring: %v\n%s", err, out)
	}
}
