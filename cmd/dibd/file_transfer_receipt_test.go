// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Observe the constructor-owned ciphertext temp, not a flag set by the test.
// SIO flushes a full 64KiB segment only after accepting at least one more byte.
// Do NOT use HEAD as a receipt probe: it takes the same ticket lock as PATCH
// and can win before PATCH arrives, causing the very upload it observes to be
// refused with 409. Only observe the store; HEAD is safe after the raw close.
func waitForDaemonUploadReceipt(t *testing.T, f *cloudFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var largest int64
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
			largest = max(largest, info.Size())
			if info.Size() > 64*1024 {
				return // a real encrypted package reached the daemon's file
			}
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("setup: no encrypted package staged before cutting the connection (largest stage %d bytes): %v", largest, ctx.Err())
		}
	}
}

const (
	uploadSetupFailureEnv    = "DIBS_TEST_UPLOAD_SETUP_FAILURE"
	uploadSetupFailureMarker = "deliberate upload setup failure with an active incomplete request"
)

// Enter through the same TLS/public-listener path as the resumability test.
// A setup Fatal must close its socket before httptest.Server.Close tries to
// drain its incomplete body. A timeout or an earlier setup failure is NOT proof.
func TestPublicTransferSetupFailureClosesRawConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPublicTransferResumesAfterBrokenConnection$", "-test.timeout=30s")
	child.Env = append(os.Environ(), uploadSetupFailureEnv+"=1")
	out, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || ctx.Err() != nil ||
		!strings.Contains(string(out), uploadSetupFailureMarker) || strings.Contains(string(out), "test timed out") {
		t.Fatalf("setup failure did not exit normally through cleanup: %v\n%s", err, out)
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
