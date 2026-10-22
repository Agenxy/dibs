// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestArchiveVerifiesWholeAppService(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "Dibs.app")
	files := map[string]file{
		"dibd":                         {archiveIdentityImage(t, "org.agenxy.dibs.daemon"), 0o755},
		notifierExecutable:             {archiveIdentityImage(t, "org.agenxy.dibs"), 0o755},
		"Dibs.app/Contents/Info.plist": {[]byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>org.agenxy.dibs</string><key>CFBundleExecutable</key><string>dibs-notify</string><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleVersion</key><string>1</string></dict></plist>`), 0o644},
		serviceManifest:                {[]byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>org.agenxy.dibs.daemon</string><key>BundleProgram</key><string>Contents/MacOS/dibd</string><key>ProgramArguments</key><array><string>Contents/MacOS/dibd</string><string>-app-service</string></array><key>AssociatedBundleIdentifiers</key><array><string>org.agenxy.dibs</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ProcessType</key><string>Interactive</string></dict></plist>`), 0o644},
	}
	files[bundledDaemon] = files["dibd"]
	for name, item := range files {
		at := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(at), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(at, item.body, os.FileMode(item.mode)); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--identifier", "org.agenxy.dibs", "--timestamp=none", app).CombinedOutput(); err != nil {
		t.Fatalf("seal fixture: %v %s", err, out)
	}
	if err := filepath.WalkDir(app, func(path string, item fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if item.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[rel] = file{body, int64(info.Mode().Perm())}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"dibd", bundledDaemon, notifierExecutable}
	check := func(name string, contents map[string]file) error {
		archive := filepath.Join(t.TempDir(), "dibs_"+name+"_darwin_"+runtime.GOARCH+".tar.gz")
		writeArchive(t, archive, contents)
		return carries(archive, want)
	}
	if err := check("sound", files); err != nil {
		t.Fatal("positive control:", err)
	}
	for _, tc := range []struct{ name, member, old, new, diagnosis string }{
		{"missing-service", serviceManifest, "", "", "manifest"},
		{"wrong-service", serviceManifest, "-app-service", "-check", "ProgramArguments"},
		{"broken-seal", "Dibs.app/Contents/Info.plist", "<string>1</string>", "<string>2</string>", "seal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := make(map[string]file, len(files))
			for name, item := range files {
				changed[name] = item
			}
			if tc.old == "" {
				delete(changed, tc.member)
			} else {
				item := changed[tc.member]
				item.body = []byte(strings.Replace(string(item.body), tc.old, tc.new, 1))
				changed[tc.member] = item
			}
			if err := check(tc.name, changed); err == nil || !strings.Contains(err.Error(), tc.diagnosis) {
				t.Fatalf("wanted %s refusal, got %v", tc.diagnosis, err)
			}
		})
	}
	t.Run("stale-paired-daemon", func(t *testing.T) {
		changed := make(map[string]file, len(files))
		for name, item := range files {
			changed[name] = item
		}
		body, err := os.ReadFile("/usr/bin/true")
		if err != nil {
			t.Fatal(err)
		}
		changed[bundledDaemon] = file{body, 0o755}
		if err := check("stale", changed); err == nil || !strings.Contains(err.Error(), "mismatched daemon sections") {
			t.Fatalf("stale paired daemon: %v", err)
		}
	})
}
