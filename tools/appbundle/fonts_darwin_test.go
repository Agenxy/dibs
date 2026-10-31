// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Build through the actual app-bundle entry and execute only its existing
// non-posting status verb. This catches a font copier that exists but is never
// called. Ad-hoc signing a private fixture never asks for a signing identity,
// notification permission or access to the real service.
func TestBuiltNotifierLoadsItsPinnedBundledFontsWithoutPosting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bundle := filepath.Join(t.TempDir(), "Dibs.app")
	src, err := filepath.Abs("../../internal/notify/app")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(ctx, "go", "run", ".", "-o", bundle, "-src", src)
	build.Env = append(os.Environ(), "DIBS_CODESIGN_IDENTITY=-")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("real appbundle: %v\n%s", err, out)
	}
	for name, want := range map[string]string{
		"AtkinsonHyperlegibleNext[wght].ttf": "5a455d1cfa099b601ab70751bb9673e8fe1854dc4500c80e1a220d0d75e31745",
		"AtkinsonHyperlegibleMono[wght].ttf": "5ce8b1698d1ded7dff2178c1a3ad159470085a58ea239e8b2cb88f4fb4a6f646",
		"OFL-Next.txt":                       "aca6a428580965d2297d1b718042dd427c2a9443ece3b0d02d758e161e0c4030",
		"OFL-Mono.txt":                       "1ebb31cf7393164f20d10c1d48406cddb5314feff8465531cf1e4ba37e9dd740",
	} {
		body, err := os.ReadFile(filepath.Join(bundle, "Contents", "Resources", "Fonts", name))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != want {
			t.Fatalf("bundled %s is not the pinned unmodified upstream file", name)
		}
	}
	probe := exec.CommandContext(ctx, filepath.Join(bundle, "Contents", "MacOS", "dibs-notify"), "--status")
	probe.Env = append(os.Environ(), "DIBS_NOTIFY_PROMPT_V1=1")
	out, err := probe.Output()
	if err != nil {
		t.Fatalf("non-posting actual helper status: %v %s", err, out)
	}
	var report struct {
		Reply, BundledFonts int
		Fonts               []string
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	if report.Reply != 1 || report.BundledFonts != 2 || len(report.Fonts) != 2 {
		t.Fatalf("packaged fonts were not registered by shipping helper: %s", out)
	}
}
