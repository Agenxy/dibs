// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package clientdrift inspects local client versions without launching them.
package clientdrift

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/agenxy/dibs/internal/release"
)

// BridgeUpdate is the remediation for a session using an old bridge.
const (
	BridgeUpdate = "Restart the session to load the updated Dibs bridge."
	// PathUpdate is the remediation for a stale PATH entry.
	PathUpdate = "Remove the stale copy from PATH or update it to the daemon's version."
)

// Binary identifies an older Dibs executable found on PATH.
type Binary struct {
	Path    string
	Version string
}

// Description names the stale path and the observed version.
func (b Binary) Description() string {
	return fmt.Sprintf("%s is an older Dibs binary (%s)", b.Path, b.Version)
}

// OlderBinaries reads Go build metadata. PATH may contain unrelated executables
// or scripts with the same basename; none are run and none are assumed to be Dibs.
func OlderBinaries(path, daemonBuild string) []Binary {
	if !release.Comparable(daemonBuild) {
		return nil
	}
	var older []Binary
	seen := make(map[string]bool)
	for _, dir := range filepath.SplitList(path) {
		dir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		for _, name := range []string{"dibs", "dibd"} {
			file := name
			if runtime.GOOS == "windows" {
				file += ".exe"
			}
			candidate := filepath.Join(dir, file)
			if seen[candidate] {
				continue
			}
			seen[candidate] = true
			version, linked := binaryVersion(candidate, name)
			if !linked && sameReleaseCore(version, daemonBuild) {
				// Go drops linker flags in trimpath builds. The module version
				// may be a same-release pseudo-version under a stamped binary.
				continue
			}
			if order, err := release.Compare(version, daemonBuild); err == nil && order < 0 {
				older = append(older, Binary{Path: candidate, Version: version})
			}
		}
	}
	return older
}

func binaryVersion(path, name string) (version string, linked bool) {
	info, err := os.Stat(path) // #nosec G703 -- metadata inspection of PATH entries
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", false
	}
	bin, err := os.Open(path) // #nosec G304 G703 -- reads build metadata only, never executes the candidate
	if err != nil {
		return "", false
	}
	defer func() { _ = bin.Close() }()
	b, err := buildinfo.Read(bin)
	if err != nil || b.Main.Path != "github.com/agenxy/dibs" || b.Path != b.Main.Path+"/cmd/"+name {
		return "", false
	}
	const stamp = "github.com/agenxy/dibs/internal/build.Version="
	for _, setting := range b.Settings {
		if setting.Key != "-ldflags" {
			continue
		}
		for _, arg := range strings.Fields(setting.Value) {
			if value, ok := strings.CutPrefix(arg, stamp); ok {
				return value, true
			}
		}
	}
	return strings.TrimPrefix(b.Main.Version, "v"), false
}

func sameReleaseCore(a, b string) bool {
	a, _, _ = strings.Cut(strings.TrimPrefix(a, "v"), "-")
	b, _, _ = strings.Cut(strings.TrimPrefix(b, "v"), "-")
	a, _, _ = strings.Cut(a, "+")
	b, _, _ = strings.Cut(b, "+")
	return a == b
}

// VersionLabel bounds and filters version evidence before it is displayed.
func VersionLabel(value string) string {
	const versionChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz.+-"
	if len(value) > 128 || strings.IndexFunc(value, func(r rune) bool {
		return !strings.ContainsRune(versionChars, r)
	}) >= 0 {
		return ""
	}
	return value
}
