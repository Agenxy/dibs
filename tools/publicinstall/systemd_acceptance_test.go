// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServiceBoardReadsActualResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"continuity", `{"node":"original-board","serial":437}`, http.StatusOK, false},
		{"malformed", `{`, http.StatusOK, true},
		{"refused", `{"node":"untrusted","serial":999}`, http.StatusForbidden, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet || req.URL.Path != "/api/board" || req.Header.Get("X-Dibs-Local") != "fixture-secret" {
					t.Error("continuity request did not reach the authenticated board endpoint")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			r := &runner{ctx: context.Background()}
			board, err := r.serviceBoard(server.Client(), strings.TrimPrefix(server.URL, "http://"), "fixture-secret")
			if (err != nil) != tc.wantError {
				t.Fatalf("wrong response result: %+v %v", board, err)
			}
			if !tc.wantError && (board.Node != "original-board" || board.Serial != 437) {
				t.Fatalf("decoded board evidence was lost: %+v", board)
			}
		})
	}
}

func TestSystemdProcessEvidenceRequiresActiveEpochAndUnit(t *testing.T) {
	valid := "MainPID=1234\nActiveState=active\nExecMainStartTimestampMonotonic=984321\nFragmentPath=/fixture/dibs.service\n"
	state, err := parseSystemdState(valid)
	if err != nil || state.PID != 1234 || state.Start != "984321" || state.Unit != "/fixture/dibs.service" {
		t.Fatalf("valid process evidence refused: %+v %v", state, err)
	}
	for _, bad := range []string{
		strings.Replace(valid, "MainPID=1234", "MainPID=0", 1),
		strings.Replace(valid, "ActiveState=active", "ActiveState=inactive", 1),
		strings.Replace(valid, "Monotonic=984321", "Monotonic=0", 1),
		strings.Replace(valid, "/fixture/dibs.service", "dibs.service", 1),
		valid + "MainPID=9999\n", "not-properties",
	} {
		if _, err := parseSystemdState(bad); err == nil {
			t.Fatalf("incomplete/ambiguous service evidence accepted: %q", bad)
		}
	}
}
