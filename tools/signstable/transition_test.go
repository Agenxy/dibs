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

func TestDaemonIdentityTransitionStillRefusesAndExplainsPermissionReview(t *testing.T) {
	const was = `identifier "org.agenxy.dibs" and certificate leaf = H"fixture"`
	const next = `identifier "org.agenxy.dibs.daemon" and certificate leaf = H"fixture"`
	dest := t.TempDir()
	b, err := json.Marshal(map[string]string{"dibd": was})
	if err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(dest, stampName)
	if err := os.WriteFile(stamp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	err = compare(dest, map[string]string{"dibd": next}, false)
	if err == nil {
		t.Fatal("intentional identity migration silently passed the permission-preservation guard")
	}
	for _, hint := range []string{"intentional one-time identity change", "Privacy & Security", "dibs doctor", "task install"} {
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("missing corrective hint %q: %v", hint, err)
		}
	}
	b, err = os.ReadFile(stamp)
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string]string
	if err := json.Unmarshal(b, &recorded); err != nil || recorded["dibd"] != next {
		t.Fatalf("existing refusal/stamp semantics changed: %v %s", err, b)
	}
}

func TestCertificateChangeIsNotDescribedAsIntentionalDaemonMigration(t *testing.T) {
	was := map[string]string{"dibd": `identifier "org.agenxy.dibs" and certificate leaf = H"old"`}
	next := map[string]string{"dibd": `identifier "org.agenxy.dibs.daemon" and certificate leaf = H"new"`}
	dest := t.TempDir()
	b, err := json.Marshal(was)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, stampName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := compare(dest, next, false); err == nil || strings.Contains(err.Error(), "intentional") {
		t.Fatalf("certificate change must retain ordinary identity refusal: %v", err)
	}
}
