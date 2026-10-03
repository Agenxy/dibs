package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBoardCLIUsesEvidenceProvenance(t *testing.T) {
	for _, source := range []string{"boot_grace", "authenticated_contact"} {
		t.Run(source, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/board" {
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"serial": 1, "node": "seen-proof",
					"agents": []map[string]any{{
						"id": "worker", "status": "active",
						"last_seen": time.Now(), "seen_source": source,
					}},
				})
			}))
			defer server.Close()
			t.Setenv("DIBS_DIR", t.TempDir())
			t.Setenv("DIBS_ADDR", strings.TrimPrefix(server.URL, "http://"))
			out, err := captureStdout(t, func() error { return board(nil) })
			if err != nil {
				t.Fatal(err)
			}
			if source == "boot_grace" {
				if !strings.Contains(out, "boot grace") || strings.Contains(out, "seen ") {
					t.Error("production CLI described boot grace as a sighting:", out)
				}
			} else if !strings.Contains(out, "contact ") {
				t.Error("production CLI lost real-contact label:", out)
			}
			wire, err := captureStdout(t, func() error { return board([]string{"--json"}) })
			var decoded struct {
				Agents []map[string]any `json:"agents"`
			}
			if err == nil {
				err = json.Unmarshal([]byte(wire), &decoded)
			}
			if err != nil || len(decoded.Agents) != 1 || decoded.Agents[0]["seen_source"] != source {
				t.Error("CLI JSON lost the daemon's provenance:", err, wire)
			}
		})
	}
}
