// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Enter through doctor and its board fetch, so a disconnected diagnostic
// helper cannot satisfy the guard.
func TestDoctorReportsExhaustedContactFailureFromBoardPayload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("fixture-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var boardReads atomic.Int32
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/board" {
			boardReads.Add(1)
			_, _ = w.Write([]byte(`{"agents":[],"contact_alerts":[{"recipient":"worker","delivery_failure":"fixture: permission denied","retry_exhausted":true}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer daemon.Close()
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", strings.TrimPrefix(daemon.URL, "http://"))
	out, _ := captureStdout(t, func() error { return doctor([]string{"--json"}) })
	if boardReads.Load() == 0 {
		t.Fatal("setup: doctor never read the fixture board")
	}
	if !strings.Contains(out, "human notification for worker failed twice: fixture: permission denied") || !strings.Contains(out, "human-relay") {
		t.Fatalf("doctor hid the failure or remedy: %s", out)
	}
}
