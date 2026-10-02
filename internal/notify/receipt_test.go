package notify

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestReceiptChild(t *testing.T) {
	mode := os.Getenv("DIBS_RECEIPT_TEST_CHILD")
	if mode == "" {
		return
	}
	path := os.Getenv("DIBS_NOTIFY_RECEIPT")
	if runtime.GOOS != "windows" {
		for _, check := range []struct {
			path string
			mode os.FileMode
		}{
			{path, 0o600}, {filepath.Dir(path), 0o700},
		} {
			info, err := os.Stat(check.path)
			if err != nil || info.Mode().Perm() != check.mode {
				os.Exit(2)
			}
		}
	}
	if mode == "escape" {
		if err := os.Remove(path); err != nil {
			os.Exit(2)
		}
		if err := os.Symlink(os.Getenv("DIBS_RECEIPT_OUTSIDE"), path); err != nil {
			os.Exit(2)
		}
	}
	if mode == "new" {
		if err := os.WriteFile(os.Getenv("DIBS_NOTIFY_RECEIPT"), []byte(`{"state":"posted"}`), 0o600); err != nil {
			os.Exit(2)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		if err := os.WriteFile(os.Getenv("DIBS_NOTIFY_RECEIPT"), []byte(`{"state":"dismissed"}`), 0o600); err != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func asReceiptHelper() {
	if len(os.Args) == 2 && os.Args[1] == "--settings" {
		_, _ = os.Stdout.WriteString("timeSensitive=1\n")
		return
	}
	if err := os.WriteFile(os.Getenv("DIBS_NOTIFY_RECEIPT"), []byte(`{"state":"posted"}`), 0o600); err != nil {
		os.Exit(2)
	}
	for i, arg := range os.Args {
		if arg == "--out" && i+1 < len(os.Args) {
			if err := os.WriteFile(os.Args[i+1], []byte("Yes"), 0o600); err != nil {
				os.Exit(2)
			}
		}
	}
	_, _ = os.Stdout.WriteString("Yes\n")
}

func TestWindowReceiptUsesTheActualGUICommand(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchctl asuser is macOS only")
	}
	t.Setenv("DIBS_TEST_RECEIPT_PUBLIC", "1")
	t.Setenv("DIBS_DIR", t.TempDir())
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "dibs-notify")
	if err := os.WriteFile(helper, bytes, 0o700); err != nil {
		t.Fatal(err)
	}
	state := ""
	choice, err := askInAWindowWithReceipt(helper, "fixture", "fixture", []string{"Yes"}, func(s string) { state = s })
	if err != nil || choice != "Yes" || state != "posted" {
		t.Fatalf("GUI command receipt: choice=%q state=%q err=%v", choice, state, err)
	}
}

// Enter through the public notifier API and its actual installed-helper
// lookup, so a receipt reader wired to neither producer cannot pass this test.
func TestPublicNotificationReceiptUsesTheInstalledHelper(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS installed-helper route")
	}
	if os.Getenv("DIBS_TEST_RECEIPT_PUBLIC") != "" {
		for _, api := range []string{"banner", "ask"} {
			state := ""
			receipt := func(s string) { state = s }
			var err error
			if api == "banner" {
				err = BannerWithReceipt("fixture", "", "fixture", receipt)
			} else {
				var choice string
				choice, err = AskWithReceipt("fixture", "fixture", receipt, "Yes")
				if choice != "Yes" {
					t.Fatalf("ask choice: %q, %v", choice, err)
				}
			}
			if err != nil || state != "posted" {
				t.Fatalf("%s receipt: %q %v", api, state, err)
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
	cmd := exec.CommandContext(ctx, driver, "-test.run=^TestPublicNotificationReceiptUsesTheInstalledHelper$") // #nosec G204 -- private copy of this test binary
	cmd.Env = append(os.Environ(), "DIBS_TEST_RECEIPT_PUBLIC=1", "DIBS_DIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notifier producer wiring: %v\n%s", err, out)
	}
}

func TestReceiptArrivesBeforeAQuestionFinishesAndOldHelpersStayUnknown(t *testing.T) {
	for _, mode := range []string{"new", "old", "escape"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "escape" && runtime.GOOS == "windows" {
				t.Skip("symlink creation requires a Windows privilege")
			}
			t.Setenv("DIBS_RECEIPT_TEST_CHILD", mode)
			t.Setenv("DIBS_DIR", t.TempDir())
			outside := filepath.Join(t.TempDir(), "forged-receipt")
			if err := os.WriteFile(outside, []byte(`{"state":"posted"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DIBS_RECEIPT_OUTSIDE", outside)
			input, release, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close(); _ = release.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReceiptChild$") // #nosec G204 -- this test binary
			cmd.Stdin = input
			receipts, done := make(chan string, 4), make(chan error, 1)
			go func() { _, err := outputWithReceipt(cmd, func(s string) { receipts <- s }); done <- err }()
			if mode == "new" {
				select {
				case state := <-receipts:
					if state != "posted" {
						t.Fatal(state)
					}
				case <-ctx.Done():
					t.Fatal("no posting receipt before answer")
				}
				select {
				case err := <-done:
					t.Fatalf("finished before release: %v", err)
				default:
				}
				if err := release.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if mode == "new" {
				if state := <-receipts; state != "dismissed" {
					t.Fatal(state)
				}
			} else {
				select {
				case state := <-receipts:
					t.Fatalf("old helper invented %q", state)
				default:
				}
			}
		})
	}
}
