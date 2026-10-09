// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExistingWakeIdleDelayLoadsWithoutTakingEffect(t *testing.T) {
	for _, value := range []string{`"10m"`, `"0s"`, `""`, `0`, `"-1s"`, `"soon"`} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte("[wake]\nopen_app_after_idle = "+value+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(dir)
			if err != nil {
				t.Fatal("retired operator key prevented boot:", err)
			}
			// Field absence is a static property, unlike conditional delivery.
			if _, exists := reflect.TypeOf(cfg.Wake).FieldByName("OpenAppAfterIdle"); exists {
				t.Fatal("retired key is still a live configuration field")
			}
		})
	}
}

func TestControlledWritersRefuseWakeIdleDelayWithoutWriting(t *testing.T) {
	for _, writer := range []string{"override", "toml"} {
		t.Run(writer, func(t *testing.T) {
			dir := t.TempDir()
			var err error
			if writer == "override" {
				err = SaveOverride(dir, "wake.open_app_after_idle", "0s", "fixture")
			} else {
				err = WriteNew(dir, []byte("[wake]\nopen_app_after_idle = \"0s\"\n"))
			}
			if err == nil || !strings.Contains(err.Error(), "removed") || !strings.Contains(err.Error(), "Remove the open_app_after_idle line") {
				t.Errorf("writer lacked corrective refusal: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("refused key wrote configuration: %v %v", entries, err)
			}
		})
	}
}
