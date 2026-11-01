// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/adminpw"
	"github.com/agenxy/dibs/internal/core"
)

// Use the real administrative handler behind its real auth gate. DIBS_ADMIN
// and possession of a bridge's local secret are not human authority.
func TestDesktopApprovalSettingRequiresHumanHTTPAuthority(t *testing.T) {
	eng, ctx := testEngine(t)
	dir := t.TempDir()
	hash, err := adminpw.Hash("test-human-password")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "admin.hash")
	if err := os.WriteFile(path, []byte(hash), 0o600); err != nil {
		t.Fatal(err)
	}
	g := newAuthGate("test-local-secret", path, "127.0.0.1:4777")
	g.sessions["test-session"] = boardSession{exp: time.Now().Add(time.Hour), page: "test-page-key"}
	mux := http.NewServeMux()
	registerAdminAPI(mux, eng)
	handler := g.wrap(mux)
	before, err := eng.DesktopApprovalSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string, headers map[string]string, cookie bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4777/api/admin/desktop-approvals", strings.NewReader(body))
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		if cookie {
			r.AddCookie(&http.Cookie{Name: g.sessionCookieName(), Value: "test-session"})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		name    string
		headers map[string]string
		cookie  bool
	}{
		{"local-secret-only", map[string]string{"X-Dibs-Local": "test-local-secret"}, false},
		{"bearer-secret", map[string]string{"Authorization": "Bearer test-local-secret"}, false},
		{"stolen-cookie-forged-origin", map[string]string{"Origin": "http://127.0.0.1:4777"}, true},
		{"wrong-page-key", map[string]string{pageKeyHeader: "wrong", "Origin": "http://127.0.0.1:4777"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := post(`{"enabled":true}`, tc.headers, tc.cookie)
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Fatalf("non-human setting change reached handler: %d %s", w.Code, w.Body)
			}
		})
	}
	read := post(`{}`, map[string]string{pageKeyHeader: "test-page-key"}, true)
	var result map[string]any
	if err := json.Unmarshal(read.Body.Bytes(), &result); err != nil || read.Code != 200 || result["value"] != "false" || result["serial"] != float64(before["serial"].(uint64)) {
		t.Fatalf("read must preserve untouched board, without minting human: %d %v %v", read.Code, result, err)
	}
	for _, tc := range []struct {
		body, want string
		headers    map[string]string
		cookie     bool
	}{
		{`{"enabled":true}`, "true", map[string]string{pageKeyHeader: "test-page-key"}, true},
		{`{"enabled":false}`, "false", map[string]string{"X-Dibs-Local": "test-local-secret", "X-Dibs-Admin": "test-human-password"}, false},
	} {
		w := post(tc.body, tc.headers, tc.cookie)
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result["setting"] != core.DesktopApprovalSettingKey || result["value"] != tc.want || result["by"] == "" {
			t.Fatalf("human setting change: %d %v %v", w.Code, result, err)
		}
		stored, err := eng.DesktopApprovalSettings(ctx)
		if err != nil || stored["value"] != tc.want || stored["by"] != result["by"] {
			t.Fatalf("handler reported an unapplied preference: %v %v", stored, err)
		}
	}
}
