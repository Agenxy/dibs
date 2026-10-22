// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorReportsGoneQueueThreadFromBoard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[wake.exec.codex]\nargv = [\"codex\", \"queue\", \"--thread\", \"{thread}\", \"--message\", \"{message}\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"gone", "unknown"} {
		t.Run(state, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{"agents": []map[string]any{{
				"id": "worker", "kind": "persistent", "status": "dormant", "resumable": true,
				"agent":      map[string]string{"harness": "Codex", "surface": "ChatGPT App", "cwd": dir},
				"queue_wake": map[string]string{"thread_state": state},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			var b boardView
			if err = json.Unmarshal(data, &b); err != nil {
				t.Fatal(err)
			}
			var oks, warns []string
			checkWakeRoutes(dir, &b, hubHosts{}, func(s string) { oks = append(oks, s) },
				func(s, fix string) { warns = append(warns, s+" "+fix) })
			gone := false
			for _, warn := range warns {
				gone = gone || strings.Contains(warn, "app thread gone")
			}
			if gone != (state == "gone") {
				t.Fatalf("doctor gone=%v for %s: oks=%v warns=%v", gone, state, oks, warns)
			}
			if state == "gone" {
				for _, ok := range oks {
					if strings.Contains(ok, "covering all 1") {
						t.Fatalf("doctor still counts gone route as covered: %v", oks)
					}
				}
			}
		})
	}
}
