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

// Enter doctor itself and fetch the board payload: a diagnostic helper that
// nobody calls must not pass this guard.
func TestDoctorReportsPendingReconnectAndClearsAfterRecovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("fixture-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var recovered atomic.Bool
	var reads atomic.Int32
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/board" {
			reads.Add(1)
			if recovered.Load() {
				_, _ = w.Write([]byte(`{"agents":[]}`))
			} else {
				_, _ = w.Write([]byte(`{"agents":[],"reconnect_pending":7,"reconnect_advice":"The next local MCP event retries these observations together."}`))
			}
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer daemon.Close()
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", strings.TrimPrefix(daemon.URL, "http://"))
	out, _ := captureStdout(t, func() error { return doctor([]string{"--json"}) })
	if reads.Load() == 0 {
		t.Fatal("setup: doctor never fetched the board")
	}
	if !strings.Contains(out, "7 pending bridge observations") || !strings.Contains(out, "next local MCP event") {
		t.Fatalf("doctor hid pending count/remedy: %s", out)
	}
	recovered.Store(true)
	out, _ = captureStdout(t, func() error { return doctor([]string{"--json"}) })
	if strings.Contains(out, "pending bridge observations") {
		t.Fatalf("doctor retained a recovered fault: %s", out)
	}
}
