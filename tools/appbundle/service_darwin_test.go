// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"debug/macho"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Build through run(), not the payload setter. This guards both the native
// service modes compiling and the build actually shipping their plist/code.
// No service is registered and no permission is requested.
func TestActualBundleBuildCarriesItsServiceAndFinalReleaseImage(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "Dibs.app")
	t.Setenv("DIBS_CODESIGN_IDENTITY", "-")
	args := os.Args
	t.Cleanup(func() { os.Args = args })
	os.Args = []string{"appbundle", "-o", app, "-src", "../../internal/notify/app", "-daemon", os.Args[0], "-presence", os.Args[0]}
	if err := run(); err != nil {
		t.Fatal("actual bundle build:", err)
	}
	for _, relative := range []string{"Contents/MacOS/dibd", "Contents/MacOS/dibs-presence", "Contents/Library/LaunchAgents/org.agenxy.dibs.daemon.plist"} {
		if info, err := os.Stat(filepath.Join(app, relative)); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("actual bundle omitted %s: %v %v", relative, info, err)
		}
	}
	plist := filepath.Join(app, "Contents", "Library", "LaunchAgents", servicePlistName)
	out, err := exec.Command("/usr/bin/plutil", "-extract", "BundleProgram", "raw", "-o", "-", plist).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "Contents/MacOS/dibd" {
		t.Fatalf("service does not name bundled daemon: %v %s", err, out)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", "--deep", app).CombinedOutput(); err != nil {
		t.Fatalf("actual bundled signature: %v %s", err, out)
	}
	// The release post hook must replace the initial placeholder with the
	// FINAL image. Comparing text sections tolerates a fresh code signature.
	final := filepath.Join(root, "dibd")
	body, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(final, body, 0o700); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"appbundle", "-embed-daemon", final, "-presence", args[0], "-o", app}
	if err := run(); err != nil {
		t.Fatal("actual release embed:", err)
	}
	text := func(path string) []byte {
		t.Helper()
		f, err := macho.Open(path)
		if err != nil {
			fat, fatErr := macho.OpenFat(path)
			if fatErr != nil {
				t.Fatal(err, fatErr)
			}
			defer func() { _ = fat.Close() }()
			f = fat.Arches[0].File
		}
		defer func() { _ = f.Close() }()
		section := f.Section("__text")
		if section == nil {
			t.Fatal("fixture has no executable text:", path)
		}
		body, err := section.Data()
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	if !bytes.Equal(text(final), text(filepath.Join(app, "Contents", "MacOS", "dibd"))) {
		t.Fatal("release post hook left the pre-build daemon in the bundle")
	}
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", "--deep", app).CombinedOutput(); err != nil {
		t.Fatalf("post-embed signature: %v %s", err, out)
	}
}
