// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"debug/macho"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const servicePlistName = "org.agenxy.dibs.daemon.plist"

const servicePlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>org.agenxy.dibs.daemon</string>
<key>BundleProgram</key><string>Contents/MacOS/dibd</string>
<key>ProgramArguments</key><array><string>Contents/MacOS/dibd</string><string>-app-service</string></array>
<key>AssociatedBundleIdentifiers</key><array><string>org.agenxy.dibs</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ProcessType</key><string>Interactive</string>
</dict></plist>
`

func writeAppService(out, daemon, presence string) error {
	plists := filepath.Join(out, "Contents", "Library", "LaunchAgents")
	if err := os.MkdirAll(plists, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(plists, servicePlistName), []byte(servicePlist), 0o600); err != nil {
		return err
	}
	for _, artifact := range []struct{ src, name, id string }{
		{daemon, "dibd", "org.agenxy.dibs.daemon"}, {presence, "dibs-presence", "org.agenxy.dibs.presence"},
	} {
		if artifact.src == "" {
			continue
		} // release builds embed the final Go artifact in its post hook
		body, err := os.ReadFile(artifact.src) // #nosec G304 -- build-tool input flag
		if err != nil {
			return err
		}
		to := filepath.Join(out, "Contents", "MacOS", artifact.name)
		// #nosec G306,G703 -- fixed nested build output must retain its execute bit.
		if err := os.WriteFile(to, body, 0o750); err != nil {
			return err
		}
		// Sign the nested code before sealing its parent. A source build's Go
		// image is still ad-hoc here; the enclosing signature alone cannot give
		// the daemon its stable, distinct code identifier.
		args := []string{"--force", "--identifier", artifact.id, "--sign", identity(), "--timestamp=none", to}
		// #nosec G204 -- fixed codesign command and build-tool input/output paths
		if out, err := exec.Command("codesign", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("signing bundled %s: %w: %s", artifact.name, err, out)
		}
	}
	return nil
}

// GoReleaser invokes every target's post hook. Only the actual Darwin image
// may replace the bundled daemon; an ELF image is an explicit no-op.
func embedReleaseDaemon(out, daemon, presence string) error {
	body, err := os.ReadFile(daemon) // #nosec G304 -- GoReleaser artifact flag
	if err != nil {
		return err
	}
	if bytes.HasPrefix(body, []byte{0x7f, 'E', 'L', 'F'}) {
		return nil
	}
	if _, err := macho.NewFile(bytes.NewReader(body)); err != nil {
		if _, fatErr := macho.NewFatFile(bytes.NewReader(body)); fatErr != nil {
			return fmt.Errorf("%s is neither a Darwin image nor an ELF release target", daemon)
		}
	}
	if err := writeAppService(out, daemon, presence); err != nil {
		return err
	}
	return signApp(out)
}

func signApp(out string) error {
	// #nosec G204 -- build-tool identity and output path, never participant input
	cmd := exec.Command("codesign", "--force", "--sign", identity(), "--timestamp=none", out)
	if body, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("codesign: %w: %s", err, body)
	}
	return nil
}
