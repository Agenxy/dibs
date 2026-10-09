// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBoardJSONRetainsUnresolvedContactPostingEvidence(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/board" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `{"agents":[],"contact_alerts":[{"serial":19,"recipient":"worker","oldest_serial":17,"high_water":18,"count":2,"window_start":"2026-10-09T17:00:00Z","notified_at":"2026-10-09T17:00:01Z"}]}`)
	}))
	defer daemon.Close()
	t.Setenv("DIBS_DIR", t.TempDir())
	t.Setenv("DIBS_ADDR", strings.TrimPrefix(daemon.URL, "http://"))
	out, err := captureStdout(t, func() error { return board([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Contacts []map[string]any `json:"contact_alerts"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Contacts) != 1 || doc.Contacts[0]["serial"] != float64(19) || doc.Contacts[0]["notified_at"] != "2026-10-09T17:00:01Z" || doc.Contacts[0]["resolved_at"] != nil {
		t.Fatalf("CLI board discarded unresolved contact evidence: %s", out)
	}
}
