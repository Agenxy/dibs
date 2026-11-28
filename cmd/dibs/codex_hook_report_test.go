// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/plugins"
	"github.com/agenxy/dibs/internal/procexec"
)

func TestReadOnlyCodexHookReportVerifiesSourceWithoutTrusting(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "codex")
	if out, err := procexec.Command(t.Context(), "go", "build", "-o", bin, "./testdata/codexplugin").CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, out)
	}
	plugin, ok := plugins.For("codex")
	if !ok {
		t.Fatal("missing shipped plugin")
	}
	source := filepath.Join(home, "hooks.json")
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_HOOK_FIXTURE_SOURCE", source)
	t.Setenv("DIBS_HOOK_FIXTURE_STATUS", "modified")
	for _, equal := range []bool{true, false} {
		body := plugin.Files["hooks.json"]
		if !equal {
			body += "\n"
		}
		if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := codexHooksJSONFor(&out, bin); err != nil {
			t.Fatal(err)
		}
		var report plugins.CodexHookReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if report.Binary != bin || len(report.Hooks) != 3 {
			t.Fatalf("wrong hook report: %+v", report)
		}
		for _, h := range report.Hooks {
			if h.Trust != "modified" || h.ShippedContent != equal {
				t.Errorf("content/trust classification: %+v", h)
			}
		}
		if strings.Contains(out.String(), source) || strings.Contains(out.String(), "currentHash") {
			t.Error("report included hook source location or approval hash")
		}
		if _, err := os.Stat(filepath.Join(home, "unexpected-trust-write")); !os.IsNotExist(err) {
			t.Fatal("read-only inspection wrote hook trust", err)
		}
	}
	if err := codexHooksCmd([]string{"--json", "--trust"}); err == nil {
		t.Error("read-only report accepted --trust")
	}
}
