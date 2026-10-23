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

func TestDoctorReportsMailboxCapacityFromBoard(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("fixture-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int32
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/board" {
			reads.Add(1)
			_, _ = w.Write([]byte(`{"agents":[{"id":"worker","status":"active","mailbox_capacity":{"depth":205,"limit":256,"accepted_debt":53,"announced_fyis":103,"near_capacity":true,"hint":"call inbox() and respond(done|decline)"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer daemon.Close()
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", strings.TrimPrefix(daemon.URL, "http://"))
	out, _ := captureStdout(t, func() error { return doctor([]string{"--json"}) })
	if reads.Load() == 0 {
		t.Fatal("setup: board not read")
	}
	if !strings.Contains(out, "worker mailbox capacity 205/256") || !strings.Contains(out, "inbox()") {
		t.Fatalf("doctor hides capacity/remedy: %s", out)
	}
}
