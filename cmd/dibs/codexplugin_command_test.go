// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/testexec"
)

func TestCodexPluginUsesOnlyAStableEntryForItsOwnExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix package-manager symlink fixture")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{bin, filepath.Join(root, "image-1"), filepath.Join(root, "image-2")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	image1, image2 := filepath.Join(root, "image-1", "dibs"), filepath.Join(root, "image-2", "dibs")
	codex := filepath.Join(root, "codex-fixture")
	for _, build := range []struct{ output, source string }{{image1, "."}, {codex, "./testdata/codexplugin"}} {
		if out, err := exec.Command("go", "build", "-o", build.output, build.source).CombinedOutput(); err != nil {
			t.Fatalf("setup build: %v %s", err, out)
		}
		arg := "--help"
		if build.output == codex {
			arg = "--version"
		}
		testexec.PrepareExecutable(t, exec.Command(build.output, arg))
	}
	entry := filepath.Join(bin, "dibs")
	if err := os.Symlink(image1, entry); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[plugins.\"dibs@dibs\"]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(exe, search string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), exe, "codex-plugin", "install", "--codex", codex)
		cmd.Env = append(os.Environ(), "CODEX_HOME="+home, "DIBS_ADMIN=1", "DIBS_PLUGIN_FIXTURE=ok", "PATH="+search)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("setup install: %v %s", err, out)
		}
		base := filepath.Join(home, "plugins", "cache", "dibs", "dibs")
		entries, err := os.ReadDir(base)
		if err != nil || len(entries) != 1 {
			t.Fatalf("setup cache: %v %v", entries, err)
		}
		b, err := os.ReadFile(filepath.Join(base, entries[0].Name(), ".mcp.json"))
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Servers map[string]struct{ Command string } `json:"mcpServers"`
		}
		if err := json.Unmarshal(b, &config); err != nil {
			t.Fatal(err)
		}
		return config.Servers["dibs"].Command
	}
	search := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	command := call(entry, search)
	parent, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(parent, "dibs")
	if command != stable {
		t.Fatalf("package entry expanded into a versioned target: %q, want %q", command, stable)
	}
	if err := os.Remove(filepath.Join(home, "installer-called")); err != nil {
		t.Fatal("setup installer marker:", err)
	}
	if err := os.Rename(image1, image2); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(image2, entry); err != nil {
		t.Fatal(err)
	}
	if command := call(entry, search); command != stable {
		t.Errorf("new package target changed stable command: %q", command)
	}
	if _, err := os.Stat(filepath.Join(home, "installer-called")); !os.IsNotExist(err) {
		t.Error("moving the package target reinstalled identical plugin content")
	}
	// A different executable named dibs on PATH is not an alias of this one.
	wrong := t.TempDir()
	if err := os.WriteFile(filepath.Join(wrong, "dibs"), []byte("unrelated executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	command = call(image2, wrong+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, gotErr := os.Stat(command)
	want, wantErr := os.Stat(image2)
	if gotErr != nil || wantErr != nil || !os.SameFile(got, want) || strings.HasPrefix(command, wrong+string(os.PathSeparator)) {
		t.Fatalf("selected somebody else's PATH executable: %q %v %v", command, gotErr, wantErr)
	}
}
