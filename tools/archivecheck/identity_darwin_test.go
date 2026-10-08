// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestArchiveChecksActualIdentityContracts(t *testing.T) {
	const helper = "Dibs.app/Contents/MacOS/dibs-notify"
	for _, tc := range []struct {
		name, daemonID, helperID, bundleID string
		want                               []string
		reject                             bool
		alterExpected                      bool
	}{
		{"aligned-notifier", "org.agenxy.dibs.daemon", "org.agenxy.dibs", "org.agenxy.dibs", []string{helper}, false, false},
		{"distinct-daemon", "org.agenxy.dibs.daemon", "org.agenxy.dibs", "org.agenxy.dibs", []string{"dibd"}, false, false},
		{"retired-notifier", "org.agenxy.dibs.daemon", "org.agenxy.dibs.notify", "org.agenxy.dibs", []string{helper}, true, false},
		{"retired-daemon", "org.agenxy.dibs", "org.agenxy.dibs", "org.agenxy.dibs", []string{"dibd"}, true, false},
		{"changed-map-mismatch", "org.agenxy.dibs.daemon", "org.agenxy.dibs.wrong", "org.agenxy.dibs", []string{helper, "dibd"}, true, true},
		{"changed-map-collision", "org.agenxy.dibs", "org.agenxy.dibs", "org.agenxy.dibs", []string{helper, "dibd"}, true, true},
		{"missing-plist", "org.agenxy.dibs.daemon", "org.agenxy.dibs", "", []string{helper, "dibd"}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.alterExpected {
				// Simulate the tempting repair of changing both expected constants.
				// Enter carries, not a property helper or a manually set production flag.
				d, h := signingIdentifiers["dibd"], signingIdentifiers[helper]
				signingIdentifiers["dibd"], signingIdentifiers[helper] = tc.daemonID, tc.helperID
				t.Cleanup(func() { signingIdentifiers["dibd"], signingIdentifiers[helper] = d, h })
			}
			files := map[string]file{
				"dibd": {archiveIdentityImage(t, tc.daemonID), 0o755},
				helper: {archiveIdentityImage(t, tc.helperID), 0o755},
			}
			if tc.bundleID != "" {
				plist := fmt.Sprintf(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>%s</string></dict></plist>`, tc.bundleID)
				files["Dibs.app/Contents/Info.plist"] = file{[]byte(plist), 0o644}
			}
			archive := filepath.Join(t.TempDir(), "dibs_0.0.13_darwin_"+runtime.GOARCH+".tar.gz")
			writeArchive(t, archive, files)
			err := carries(archive, tc.want)
			if (err != nil) != tc.reject {
				t.Fatalf("actual archive identity contract: rejection=%v, want=%v: %v", err != nil, tc.reject, err)
			}
		})
	}
}

func archiveIdentityImage(t *testing.T, id string) []byte {
	t.Helper()
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(path, image, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--identifier", id,
		"--sign", "-", "--timestamp=none", path).CombinedOutput(); err != nil {
		t.Fatalf("native setup signing failed: %v %s", err, out)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", path).CombinedOutput(); err != nil {
		t.Fatalf("native setup signature invalid: %v %s", err, out)
	}
	out, err := exec.Command("/usr/bin/codesign", "-dv", path).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "\nIdentifier="+id+"\n") {
		t.Fatalf("native setup identifier wrong: %v %s", err, out)
	}
	image, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return image
}
