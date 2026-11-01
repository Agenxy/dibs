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
	"time"
)

// Keep the engine's actual ephemeral counters and its actual elapsed window.
// Enter through doctor, its HTTP read and report recording, rather than passing
// a fabricated verdict to a presentation helper. No hook is called in this test.
func TestDoctorBoundsZeroHookClaimsToItsObservedWindow(t *testing.T) {
	dir := cliIPCProfile(t)
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("fixture-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeStart := time.Now()
	eng := startupEngine(t, dir)
	started := time.Now()
	var reads atomic.Int32
	daemon := newLocalTestServer(t, dir, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/hook-health" {
			reads.Add(1)
			if r.Header.Get("X-Dibs-Local") != "fixture-secret" {
				t.Error("doctor omitted the local credential")
			}
			_ = json.NewEncoder(w).Encode(eng.HookHealth())
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer daemon.Close()
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_ADDR", strings.TrimPrefix(daemon.URL, "http://"))
	read := func(t *testing.T) doctorCheck {
		t.Helper()
		before := reads.Load()
		beforeRead := time.Now()
		out, _ := captureStdout(t, func() error { return doctor([]string{"--json"}) })
		if reads.Load() != before+1 {
			t.Fatal("setup: doctor did not read actual hook health exactly once")
		}
		var report doctorReport
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatalf("doctor output: %v: %s", err, out)
		}
		for _, check := range report.Checks {
			if strings.Contains(check.What, "hook call") || strings.Contains(check.What, "no harness has ever called") {
				window, hasAge := strings.CutPrefix(check.What, "no hook call since this daemon started ")
				window, _, hasAgo := strings.Cut(window, " ago")
				age, err := time.ParseDuration(window)
				minimum := max(0, beforeRead.Sub(started)).Truncate(time.Second)
				maximum := time.Since(beforeStart).Truncate(time.Second)
				if !hasAge || !hasAgo || err != nil || age < minimum || age > maximum {
					t.Errorf("reported observation age must be between %s and %s: %+v", minimum, maximum, check)
				}
				return check
			}
		}
		t.Fatalf("doctor hid the zero-call observation: %s", out)
		return doctorCheck{}
	}
	t.Run("fresh daemon", func(t *testing.T) {
		check := read(t)
		if time.Since(started) >= time.Minute {
			t.Fatal("setup: fresh observation already aged past one minute")
		}
		if check.Level != "note" || check.Fix != "" || !strings.Contains(check.What, "since this daemon started") || !strings.Contains(check.What, "first 1m0s") {
			t.Errorf("fresh daemon gives an installation warning instead of a bounded observation: %+v", check)
		}
	})
	t.Run("aged daemon", func(t *testing.T) {
		// Use the real interval. A test-only shortened grace or manually set
		// start stamp would miss a constructor/wiring regression.
		timer := time.NewTimer(time.Until(started.Add(61 * time.Second)))
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-t.Context().Done():
			t.Fatal("test ended before the real one-minute observation")
		}
		check := read(t)
		if check.Level != "warning" || !strings.Contains(check.Fix, "Install the plugin") || !strings.Contains(check.What, "since this daemon started") || strings.Contains(check.What, "ever") || strings.Contains(check.Fix, "ever") {
			t.Errorf("aged daemon lost its honest observation or corrective hint: %+v", check)
		}
	})
}
