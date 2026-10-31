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
)

func TestDesktopApprovalCLIRefusalsNameTheirCorrectiveCommand(t *testing.T) {
	dir := cliIPCProfile(t)
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("private-fixture-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	var reached atomic.Int32
	server := newLocalTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil || call.Params.Name != "settings" {
			t.Errorf("CLI did not enter its real settings route: %v %+v", err, call)
			http.Error(w, "wrong route", http.StatusBadRequest)
			return
		}
		reached.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"isError": true, "content": []map[string]string{
				{"type": "text", "text": `{"code":"E_OLD","message":"fixture refusal without a hint"}`},
			}},
		})
	}))
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", strings.TrimPrefix(server.URL, "http://"))
	t.Setenv("DIBS_TOKEN", "fixture-member-token")
	t.Setenv("DIBS_INVITE", "")
	reresolveMCPEndpoint()
	t.Cleanup(reresolveMCPEndpoint)
	out, err := captureStdout(t, func() error { return desktopApprovalsCmd([]string{"on", "--json"}) })
	if err == nil {
		t.Fatal("server refusal was reported as a successful setting change")
	}
	if reached.Load() != 1 {
		t.Fatalf("fixture did not reach the real settings request: %d %v", reached.Load(), err)
	}
	var fault struct {
		Code string `json:"code"`
		Hint string `json:"hint"`
	}
	if err := json.Unmarshal([]byte(out), &fault); err != nil || fault.Code != "E_OLD" ||
		!strings.Contains(fault.Hint, "desktop-approvals --help") || strings.Contains(fault.Hint, "dibs send --help") {
		t.Fatalf("setting refusal points at an unrelated command: %s %v", out, err)
	}
}
