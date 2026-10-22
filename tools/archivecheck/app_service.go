// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/agenxy/dibs/internal/machoidentity"
)

const (
	bundledDaemon   = "Dibs.app/Contents/MacOS/dibd"
	serviceManifest = "Dibs.app/Contents/Library/LaunchAgents/org.agenxy.dibs.daemon.plist"
)

func verifyAppService(archive string, have map[string]entry) error {
	manifest, ok := have[serviceManifest]
	if !ok {
		return fmt.Errorf("%s: missing app-owned service manifest %s", archive, serviceManifest)
	}
	cmd := exec.Command("plutil", "-convert", "json", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(manifest.body)
	body, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: service manifest: %w: %s", archive, err, body)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	for key, want := range map[string]any{
		"Label": "org.agenxy.dibs.daemon", "BundleProgram": "Contents/MacOS/dibd",
		"ProgramArguments":            []any{"Contents/MacOS/dibd", "-app-service"},
		"AssociatedBundleIdentifiers": []any{"org.agenxy.dibs"}, "RunAtLoad": true,
		"KeepAlive": map[string]any{"SuccessfulExit": false}, "ProcessType": "Interactive",
	} {
		if !reflect.DeepEqual(fields[key], want) {
			return fmt.Errorf("%s: app service %s = %v, want %v", archive, key, fields[key], want)
		}
	}
	if len(fields) != 7 {
		return fmt.Errorf("%s: unexpected app service manifest fields; review the service contract", archive)
	}
	if err := compareAppDaemon(archive, have); err != nil {
		return err
	}
	return verifyAppSeal(archive, have)
}

func compareAppDaemon(archive string, have map[string]entry) error {
	root, err := machoidentity.Sections(bytes.NewReader(have["dibd"].body))
	if err != nil {
		return fmt.Errorf("%s: root daemon sections: %w", archive, err)
	}
	paired, err := machoidentity.Sections(bytes.NewReader(have[bundledDaemon].body))
	if err != nil {
		return fmt.Errorf("%s: bundled daemon sections: %w", archive, err)
	}
	if root != paired {
		return fmt.Errorf("%s: mismatched daemon sections: root %s; bundle %s; rebuild the "+
			"complete distribution", archive, root, paired)
	}
	return nil
}

func verifyAppSeal(archive string, have map[string]entry) error {
	// Verify the actual enclosing seal, not a detached main executable whose
	// signature correctly requires its adjacent Info.plist and resources.
	dir, err := os.MkdirTemp("", "archivecheck-app-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for name, item := range have {
		if !strings.HasPrefix(name, "Dibs.app/") {
			continue
		}
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%s: non-local bundle member %q", archive, name)
		}
		at := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(at), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(at, item.body, os.FileMode(item.mode&0o777)); err != nil {
			return err
		}
	}
	// #nosec G204 -- private reconstructed bundle, no participant input or shell
	cmd := exec.Command("codesign", "--verify", "--deep", "--strict", filepath.Join(dir, "Dibs.app"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: invalid full Dibs.app seal: %w: %s", archive, err, out)
	}
	return nil
}
