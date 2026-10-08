// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

// Enter through doctor's status fetch and normal matching dispatch, so a
// function called only by the test cannot satisfy this guard.
func TestDoctorReportsDaemonUnreadableTreesInEveryMatchingPhase(t *testing.T) {
	for _, phase := range []string{"off", "indexing", "ready", "suggest-only", "degraded"} {
		t.Run(phase, func(t *testing.T) {
			st := matchStatusJSON{Phase: phase, Host: "local", Unreadable: []string{"/unreadable", "/shipped/pkg", "/remote", "/foreign/pkg"}, Remote: []string{"/remote"}, Supplied: map[string]string{"/shipped": "local-shipper", "/foreign": "remote-shipper"}, SuppliedHosts: map[string]string{"/shipped": "local", "/foreign": "elsewhere"}}
			raw, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client := &http.Client{Transport: guestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodGet || req.URL.Path != "/api/match-status" || req.Header.Get("X-Dibs-Local") != "fixture" {
					return nil, fmt.Errorf("unexpected status fixture request: %s %s", req.Method, req.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			var warnings []string
			checkMatching(client, "fixture", func(string) {}, func(msg, hint string) { warnings = append(warnings, msg+": "+hint) })
			if calls != 1 {
				t.Fatalf("setup did not enter actual daemon status fetch: %d calls", calls)
			}
			for _, root := range []string{"/unreadable", "/foreign/pkg"} {
				found := false
				for _, warning := range warnings {
					if strings.HasPrefix(warning, root+" cannot be read by the daemon") {
						found = true
						if !strings.Contains(warning, "cause is unknown") ||
							(runtime.GOOS == "darwin" && !strings.Contains(warning, "Privacy & Security")) {
							t.Errorf("access loss lacks honest cause/remedy: %s", warning)
						}
					}
				}
				if !found {
					t.Errorf("doctor concealed daemon access loss at %s in phase %s: %v", root, phase, warnings)
				}
			}
			for _, warning := range warnings {
				if strings.HasPrefix(warning, "/shipped/pkg cannot be read") || strings.HasPrefix(warning, "/remote cannot be read") {
					t.Errorf("doctor blamed a shipped or remote tree for local access loss: %s", warning)
				}
			}
		})
	}
}
