// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/agenxy/dibs/internal/plugins"
	"github.com/agenxy/dibs/internal/testexec"
)

func TestCodexPluginContentIdentityIncludesEveryPayloadInput(t *testing.T) {
	plugin, ok := plugins.For("codex")
	if !ok {
		t.Fatal("setup: Codex plugin missing")
	}
	base := maps.Clone(plugin.Files)
	first, err := stampCodexPlugin(base, "tools-a")
	if err != nil {
		t.Fatal(err)
	}
	for name := range plugin.Files {
		t.Run(name, func(t *testing.T) {
			changed := maps.Clone(plugin.Files)
			if name == ".codex-plugin/plugin.json" {
				var manifest map[string]any
				if err := json.Unmarshal([]byte(changed[name]), &manifest); err != nil {
					t.Fatal(err)
				}
				manifest["description"] = "a changed plugin description"
				b, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				changed[name] = string(b)
			} else {
				changed[name] += "\nchanged payload input\n"
			}
			v, err := stampCodexPlugin(changed, "tools-a")
			if err != nil {
				t.Fatal(err)
			}
			if v == first {
				t.Error("changed payload reused approved content identity")
			}
		})
	}
	changed := maps.Clone(plugin.Files)
	if v, err := stampCodexPlugin(changed, "tools-b"); err != nil || v == first {
		t.Fatalf("changed tools reused identity: %s %v", v, err)
	}
	// Re-stamping actual emitted bytes is stable; generated metadata must not
	// hash its previous value and make each inspection look like another update.
	repeated := maps.Clone(base)
	if v, err := stampCodexPlugin(repeated, "tools-a"); err != nil || v != first || !reflect.DeepEqual(base, repeated) {
		t.Fatalf("same approved bytes changed on re-stamp: %s %v", v, err)
	}
}

func TestCodexPluginBuildOnlyRefreshDoesNotInvokeInstaller(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_PLUGIN_FIXTURE", "ok")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[plugins.\"dibs@dibs\"]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "codex-fixture")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/codexplugin").CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	testexec.PrepareExecutable(t, exec.Command(bin, "--version"))
	first, err := planCodexPlugin(bin)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := first.install(&out); err != nil {
		t.Fatal("setup install:", err)
	}
	if !first.current() {
		t.Fatal("setup: initial install did not verify")
	}
	marker := filepath.Join(home, "installer-called")
	if err := os.Remove(marker); err != nil {
		t.Fatal("setup: initial installer did not run:", err)
	}
	before, err := os.Stat(filepath.Join(first.cacheBase(), first.version))
	if err != nil {
		t.Fatal(err)
	}
	previous := version
	t.Cleanup(func() { version = previous })
	version += ".unrelated-source-change"
	second, err := planCodexPlugin(bin)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := second.install(&out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("build-only refresh invoked the installer again")
	}
	after, err := os.Stat(filepath.Join(second.cacheBase(), second.version))
	if err != nil || !os.SameFile(before, after) || !reflect.DeepEqual(first.files, second.files) {
		t.Errorf("build-only refresh replaced approved files/root: %v", err)
	}
}
