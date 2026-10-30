// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/folderaccess"
)

func TestDoctorReportsTheDaemonsProtectedFolderEvidence(t *testing.T) {
	for _, state := range []string{"pending", "denied", "granted"} {
		t.Run(state, func(t *testing.T) {
			dir := cliIPCProfile(t)
			t.Setenv("HOME", dir)
			t.Setenv("CODEX_HOME", filepath.Join(dir, ".codex"))
			t.Setenv("DIBS_DIR", dir)
			t.Setenv("DIBS_NO_UPDATE_CHECK", "1")
			t.Setenv("DIBS_NOTIFY", "off")
			for name, body := range map[string]string{"local.secret": strings.Repeat("a", 64), "node_id": "folder-fixture"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var boardCalls int
			srv := newLocalTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/mcp":
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
				case "/api/board":
					boardCalls++
					_ = json.NewEncoder(w).Encode(map[string]any{"node": "folder-fixture", "agents": []any{}, "folder_access": []folderaccess.Observation{{Folder: "Desktop", State: state, Reason: "fixture directory read"}}})
				default:
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			t.Cleanup(srv.Close)
			t.Setenv("DIBS_ADDR", srv.URL)
			out, _ := captureStdout(t, func() error { return doctor([]string{"--json"}) })
			var report doctorReport
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatalf("actual doctor did not emit its JSON document: %v %s", err, out)
			}
			if boardCalls == 0 {
				t.Fatal("setup: doctor never read daemon board")
			}
			found := false
			for _, check := range report.Checks {
				if state == "granted" && check.What == "Dibs can read Desktop" {
					found = true
				}
				if state != "granted" && check.What == "Dibs Desktop access is "+state {
					found = strings.Contains(check.Fix, folderaccess.Settings) && strings.Contains(check.Fix, "signing channel")
				}
			}
			if !found {
				t.Fatalf("actual doctor did not report daemon %s evidence and corrective path: %s", state, out)
			}
		})
	}
}
