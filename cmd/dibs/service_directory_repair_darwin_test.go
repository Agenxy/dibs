// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agenxy/dibs/internal/paths"
)

func TestPersistentBridgeRefusesBrokenSelectionAndRecoversAfterRepair(t *testing.T) {
	home := cliIPCProfile(t)
	t.Setenv("HOME", home)
	t.Setenv("DIBS_DIR", "")
	t.Setenv("DIBS_ADDR", "")
	dir := filepath.Join(home, ".dibs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	srv := newLocalTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			ID any `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"board": "original"}})
	}))
	t.Cleanup(srv.Close)
	secret := filepath.Join(dir, "local.secret")
	for path, body := range map[string]string{secret: strings.Repeat("c", 64), filepath.Join(dir, "dibs.toml"): "addr = '" + srv.Listener.Addr().String() + "'\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := paths.WriteServiceDirectory(dir); err != nil {
		t.Fatal(err)
	}
	call := serviceDirectoryBridge(t)
	if got := call(1); resultBoard(got) != "original" {
		t.Fatal("setup did not reach original board:", got)
	}
	pin, err := paths.ServiceConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pin, []byte("version = 999"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := call(2); got["error"] == nil || calls.Load() != 1 {
		t.Fatalf("broken pin sent a request or killed the bridge: %v calls=%d", got, calls.Load())
	}
	if err := paths.WriteServiceDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if got := call(3); resultBoard(got) != "original" {
		t.Fatal("repaired pin did not recover:", got)
	}
	if err := os.Remove(secret); err != nil {
		t.Fatal(err)
	}
	if got := call(4); got["error"] == nil || calls.Load() != 2 {
		t.Fatalf("missing credential sent a stale value: %v calls=%d", got, calls.Load())
	}
	if err := os.WriteFile(secret, []byte(strings.Repeat("c", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := call(5); resultBoard(got) != "original" {
		t.Fatal("repaired secret did not recover:", got)
	}
}
