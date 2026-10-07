// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy
// This identifier applies only to Agenxy-authored portions.
// Outside contributions retain their original licences; see NOTICE.

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

// Enter at doctor itself, with the board response a real daemon supplies.
// Calling the diagnostic helper directly would miss an unwired reader.
func TestDoctorReportsConfiguredNameAliasesFromTheBoard(t *testing.T) {
	for _, c := range []struct {
		name, address, want, fix string
	}{
		{"alias", `{"name":"prior-role","id":"worker-id","via":"alias"}`, "former-name alias", ""},
		{"shadow", `{"name":"prior-role","id":"peer-id","via":"current","shadowed_aliases":["worker-id"]}`, "shadows former names of worker-id", "immutable agent id"},
		{"ambiguous", `{"name":"prior-role","via":"alias","ambiguous":["worker-id","peer-id"]}`, "ambiguous former name", "immutable agent id"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("fixture-secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var boardReads atomic.Int64
			daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/board" {
					boardReads.Add(1)
					_, _ = w.Write([]byte(`{"agents":[],"configured_name_addresses":[` + c.address + `]}`))
					return
				}
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
			}))
			defer daemon.Close()
			t.Setenv("DIBS_DIR", dir)
			t.Setenv("DIBS_ADDR", strings.TrimPrefix(daemon.URL, "http://"))
			out, _ := captureStdout(t, func() error { return doctor([]string{"--json"}) })
			if boardReads.Load() == 0 {
				t.Fatal("setup: doctor did not read the fixture board")
			}
			if !strings.Contains(out, c.want) || !strings.Contains(out, "prior-role") || !strings.Contains(out, c.fix) {
				t.Fatalf("doctor omitted alias/shadow diagnostic: %s", out)
			}
		})
	}
}
