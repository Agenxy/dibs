package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Observe the constructor-owned ciphertext temp, not a flag set by the test.
// SIO flushes a full 64KiB segment only after accepting at least one more byte.
// While the upload owns its ticket lock HEAD deliberately returns 409, so it
// cannot report the offset yet; its round trips provide event-driven waiting.
func waitForDaemonUploadReceipt(t *testing.T, f *cloudFixture, target string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		entries, err := os.ReadDir(filepath.Join(f.dir, "blobs"))
		if err != nil {
			t.Fatalf("setup: staging directory unavailable: %v", err)
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), ".tmp-upload-") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				t.Fatalf("setup: staging evidence unavailable: %v", err)
			}
			if info.Size() > 64*1024 {
				return // a real encrypted package reached the daemon's file
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := f.public.Client().Do(req)
		if err != nil {
			t.Fatalf("setup: no bytes staged before cutting the connection: %v", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusConflict && response.StatusCode != http.StatusNoContent {
			t.Fatalf("setup: upload refused before bytes were staged: HTTP %d", response.StatusCode)
		}
	}
}

func interruptedUploadOffset(t *testing.T, f *cloudFixture, target string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := f.public.Client().Do(req)
		if err != nil {
			t.Fatalf("interrupted upload did not release its request: %v", err)
		}
		_ = response.Body.Close()
		if response.StatusCode == http.StatusConflict {
			continue // real handler still owns the ticket lock
		}
		if response.StatusCode != http.StatusNoContent || response.Header.Get("Upload-Complete") != "?0" {
			t.Fatalf("cut request was finalized or discarded: HTTP %d", response.StatusCode)
		}
		offset, err := strconv.ParseInt(response.Header.Get("Upload-Offset"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return offset // zero is a real failure, not something to poll past
	}
}
