package notify

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestReceiptChild(t *testing.T) {
	mode := os.Getenv("DIBS_RECEIPT_TEST_CHILD")
	if mode == "" {
		return
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

func TestReceiptArrivesBeforeAQuestionFinishesAndOldHelpersStayUnknown(t *testing.T) {
	for _, mode := range []string{"new", "old"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DIBS_RECEIPT_TEST_CHILD", mode)
			t.Setenv("DIBS_DIR", t.TempDir())
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
