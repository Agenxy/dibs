// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/plugins"
)

// Only the external read-only probe is replaced. The real daemon run and
// startup monitor must call it, classify it and publish their actual warning.
func installCodexTrustFixture() {
	bin := os.Getenv("DIBS_TEST_HOOK_RUNTIME")
	if bin == "" {
		return
	}
	clientDriftNotificationAvailable = func() bool { return true }
	postClientDriftNotification = func(title, subtitle, body string) error {
		return os.WriteFile(os.Getenv("DIBS_TEST_HOOK_POSTED"), []byte(title+"\n"+subtitle+"\n"+body), 0o600)
	}
	codexTrustObserver = func() *codexTrustWatch {
		return &codexTrustWatch{
			resolve: func() string { return bin },
			inspect: func(context.Context) (plugins.CodexHookReport, error) {
				var report plugins.CodexHookReport
				b, err := os.ReadFile(os.Getenv("DIBS_TEST_HOOK_REPORT"))
				if err != nil {
					return report, err
				}
				err = json.Unmarshal(b, &report)
				return report, err
			},
		}
	}
}

func TestDaemonStartupReportsUnchangedHookTrustLoss(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "fixture-runtime")
	if err := os.WriteFile(bin, []byte("fixture runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	report := plugins.CodexHookReport{Binary: bin, Hooks: []plugins.CodexHookApproval{{Event: "Stop", Trust: "modified", ShippedContent: true}}}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "report.json")
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	posted := filepath.Join(home, "posted")
	_, stop, logs := qStartBoardCaptured(t, qShortDir(t), "DIBS_TEST_HOOK_RUNTIME="+bin, "DIBS_TEST_HOOK_REPORT="+file,
		"DIBS_TEST_HOOK_POSTED="+posted,
		"HOME="+home, "CODEX_HOME="+home, "DIBS_NOTIFY=off")
	want := "Codex reports 1 Dibs hooks as modified (source matches shipped bytes)"
	deadline := time.After(5 * time.Second)
	for {
		body, _ := os.ReadFile(posted)
		if strings.Contains(string(body), want) && strings.Contains(string(body), "`dibs codex-hooks`") {
			break
		}
		select {
		case <-deadline:
			stop()
			t.Fatalf("actual daemon startup did not submit hook approval alert and exact command: %s", logs())
		case <-time.After(10 * time.Millisecond):
		}
	}
	stop()
	if !strings.Contains(logs(), "Dibs changed no hook trust or enablement") {
		t.Error("warning claimed or omitted approval boundary")
	}
}

func TestCodexTrustProbeRunsOncePerRuntimeIdentity(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codex")
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(bin, []byte(value), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write("old")
	calls := 0
	status := "modified"
	queryFails := false
	w := codexTrustWatch{resolve: func() string { return bin }, inspect: func(context.Context) (plugins.CodexHookReport, error) {
		calls++
		if queryFails {
			return plugins.CodexHookReport{}, errors.New("fixture unavailable")
		}
		return plugins.CodexHookReport{Binary: bin, Hooks: []plugins.CodexHookApproval{
			{Event: "Stop", Trust: status, ShippedContent: true},
			{Event: "SessionStart", Trust: status, ShippedContent: false},
		}}, nil
	}}
	first := w.observe(t.Context())
	if len(first) != 2 || calls != 1 {
		t.Fatalf("startup report: %v queries=%d", first, calls)
	}
	_ = w.observe(t.Context())
	if calls != 1 {
		t.Fatal("unchanged executable repeated an app-server query")
	}
	write("new runtime")
	second := w.observe(t.Context())
	if calls != 2 || len(second) != 2 || first[0].kind == second[0].kind {
		t.Fatal("runtime replacement did not create a fresh alert identity")
	}
	status = "trusted"
	write("new trusted runtime")
	if got := w.observe(t.Context()); len(got) != 0 || calls != 3 {
		t.Fatal("trusted hooks generated an approval-loss alert", got, calls)
	}
	queryFails = true
	write("unverifiable new runtime")
	if got := w.observe(t.Context()); len(got) != 1 || !strings.Contains(got[0].what, "could not be verified") {
		t.Fatal("unknown hook state was silently approved", got)
	}
}
