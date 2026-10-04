package main

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The production install door, not a direct call to the guard. The bad stamp
// recreated the GOFLAGS override that reached the live board after PR #335.
// A normal clone has its own .git directory and lets Go retain the module's
// pseudo-version; a managed worktree did not on the measured Go build.
func TestTaskInstallKeepsTheComputedStampBeforeReplacingAnything(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("task install currently uses Unix copy/remove commands")
	}
	if testing.Short() {
		t.Skip("builds and installs both real binaries in an isolated clean clone")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	root := filepath.Join("..", "..")
	head := strings.TrimSpace(installCommand(t, ctx, root, nil, "git", "rev-parse", "HEAD"))
	scratch := t.TempDir()
	clone, dest := filepath.Join(scratch, "repo"), filepath.Join(scratch, "installed")
	if runtime.GOOS == "darwin" {
		// Normal install registers its notifier bundle. Unregister only this
		// test's exact bundle before TempDir removes it; never leave a deleted
		// fixture as another Launch Services candidate for the live product.
		t.Cleanup(func() {
			app := filepath.Join(dest, "Dibs.app")
			if _, err := os.Stat(app); os.IsNotExist(err) {
				return
			}
			cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
			defer done()
			cmd := exec.CommandContext(cleanup,
				"/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister", "-u", app)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("unregister own fixture app: %v: %s", err, out)
			}
		})
	}
	installCommand(t, ctx, root, nil, "git", "clone", "--quiet", "--no-local", ".", clone)
	installCommand(t, ctx, clone, nil, "git", "checkout", "--quiet", "--detach", head)
	// The runner is pinned by this clone's config, exactly as for a source
	// install. Never change HOME, the live install prefix or the live board.
	installCommand(t, ctx, clone, nil, "mise", "trust")
	installCommand(t, ctx, clone, nil, "mise", "install")
	env := isolatedInstallEnv(scratch, "")
	installCommand(t, ctx, clone, env, "task", "install", "DEST="+dest)
	previous := map[string][32]byte{}
	for _, name := range []string{"dibs", "dibd"} {
		path := filepath.Join(dest, name)
		info, err := buildinfo.ReadFile(path)
		if err != nil {
			t.Fatalf("installed %s build info: %v", name, err)
		}
		if !strings.HasPrefix(info.Main.Version, "v") || strings.Contains(info.Main.Version, "devel") {
			t.Fatalf("normal clean-clone %s lost its module version: %q", name, info.Main.Version)
		}
		settings := map[string]string{}
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
		if settings["vcs.revision"] != head || settings["vcs.modified"] != "false" {
			t.Fatalf("normal clone %s provenance = %v, want clean %s", name, settings, head)
		}
		if name == "dibs" {
			out := installCommand(t, ctx, clone, env, path, "version")
			first, _, _ := strings.Cut(out, "\n")
			if first != "dibs "+strings.TrimPrefix(info.Main.Version, "v") {
				t.Fatalf("normal task install changed version shape: %q vs %q", first, info.Main.Version)
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		previous[name] = sha256.Sum256(b)
	}
	cmd := exec.CommandContext(ctx, "task", "install", "DEST="+dest)
	cmd.Dir, cmd.Env = clone, isolatedInstallEnv(scratch,
		"-ldflags=-X=github.com/agenxy/dibs/internal/build.Version=devel+"+head)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "installstamp:") || !strings.Contains(string(out), "computed") {
		t.Fatalf("conflicting override must fail at the real install guard, got %v:\n%s", err, out)
	}
	for name, want := range previous {
		b, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || sha256.Sum256(b) != want {
			t.Errorf("refused install replaced %s: %v", name, err)
		}
	}
}

func installCommand(t *testing.T, ctx context.Context, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("setup %s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func isolatedInstallEnv(scratch, flags string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "DIBS_") && key != "GOFLAGS" {
			env = append(env, entry)
		}
	}
	return append(env, "GOFLAGS="+flags, "DIBS_NOTIFY=off", "DIBS_ADDR=http://127.0.0.1:1",
		"DIBS_DIR="+filepath.Join(scratch, "board"))
}
