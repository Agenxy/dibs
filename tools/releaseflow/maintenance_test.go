//go:build darwin || linux

package main

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Regression enters at fixture(), then invokes the unchanged production
// command(). Local policy must protect BOTH repositories from inherited auto
// maintenance. Merely winning the TempDir deletion race is not a green result.
func TestReleaseFixtureStartsNoAutomaticGitWriter(t *testing.T) {
	for _, strategy := range []string{"gc", "geometric"} {
		for _, target := range []string{"repo", "origin"} {
			for _, enabled := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/forced-enabled=%t", strategy, target, enabled)
				t.Run(name, func(t *testing.T) {
					probeFixtureMaintenance(t, strategy, target, enabled)
				})
			}
		}
	}
}

type fixtureMaintenanceProbe struct {
	control string
	trace   string
	gitDirs []string
}

func probeFixtureMaintenance(t *testing.T, strategy, target string, enabled bool) {
	t.Helper()
	p := fixtureMaintenanceProbe{control: t.TempDir()}
	p.trace = filepath.Join(p.control, "trace.jsonl")
	// Defer, not an early t.Cleanup: release/reap runs BEFORE fixture TempDir's
	// cleanup even on t.Fatal. The control directory outlives the repository.
	defer p.finish(t)
	global := filepath.Join(p.control, "global.gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_DEFAULT_HASH", "sha1")
	write(t, global, "[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n")
	c := fixture(t)
	repo := git(t, "rev-parse", "--absolute-git-dir")
	origin := filepath.Join(filepath.Dir(c.receiptPath), "origin.git")
	p.gitDirs = []string{repo, origin}
	gitDir := repo
	if target == "origin" {
		gitDir = origin
	}
	seedFixtureLooseObjects(t, gitDir)
	if enabled {
		// Positive control deliberately overrides fixture policy on this one
		// disposable repository. It must spawn and finish a real Git writer.
		git(t, "--git-dir="+gitDir, "config", "--local", "gc.auto", "1")
		git(t, "--git-dir="+gitDir, "config", "--local", "maintenance.auto", "true")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Git's hook is a command string. Quote only where its shell parser needs
	// it; no shell script, fake Git, or mocked maintenance process is involved.
	hook := exe
	if strings.ContainsAny(hook, " \t\n|&;<>()$`\\\"'") {
		hook = "'" + strings.ReplaceAll(hook, "'", "'\\''") + "'"
	}
	ambient := fmt.Sprintf("[gc]\n\tauto = 1\n\tautoDetach = true\n\tpruneExpire = 1.hour.ago\n\trecentObjectsHook = %s\n"+
		"[maintenance]\n\tauto = true\n\tautoDetach = true\n\tstrategy = %s\n\tgeometric-repack.auto = 1\n", strconv.Quote(hook), strategy)
	write(t, global, ambient)
	t.Setenv("DIBS_TEST_RELEASE_GC_HOOK", "1")
	t.Setenv("DIBS_TEST_RELEASE_GC_CONTROL", p.control)
	if target == "origin" {
		// Make the push nonempty without provoking local maintenance: only the
		// remote receive-pack in the actual production push is under test.
		git(t, "-c", "maintenance.auto=false", "-c", "gc.auto=0", "commit", "--allow-empty", "-m", "remote writer probe")
	}
	t.Setenv("GIT_TRACE2_EVENT", p.trace)
	args := []string{"commit", "--allow-empty", "-m", "local writer probe"}
	if target == "origin" {
		args = []string{"push", "origin", "main"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := command(ctx, nil, "git", args...)
	if err != nil {
		t.Fatalf("real Git production command setup failed: %v: %s", err, out)
	}
	started := automaticMaintenanceStarted(t, p.trace)
	if !started {
		if enabled {
			t.Fatal("positive control did not start automatic maintenance; this probe proves nothing")
		}
		t.Logf("NO AUTOMATIC WRITER: %s %s; real command returned and Trace2 recorded no auto-maintenance child", strategy, target)
		return
	}
	var report fixtureGCReport
	p.wait(t, func() bool {
		reports := p.reports(t)
		if len(reports) == 0 {
			return false
		}
		report = reports[0]
		return true
	}, "real post-detach GC hook")
	pid, process := realGitWriter(t, report.Parent)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("reported real writer is not alive after command returned: %v", err)
	}
	t.Logf("LIVE DETACHED GIT WRITER: %s %s pid=%d command=%s; production command already returned", strategy, target, pid, process)
	if !enabled {
		t.Error("fixture leaked a real automatic Git writer after production command returned")
	}
}

// gc.auto=1 alone is not a setup proof: legacy Git samples fanout 17 and Git
// 2.55 rounds the limit up to 256. Two REAL loose blobs in every SHA-1 fanout
// exceed either heuristic deterministically, without hundreds of Git processes.
func seedFixtureLooseObjects(t *testing.T, gitDir string) {
	t.Helper()
	inputs := t.TempDir()
	counts := [256]int{}
	var paths, oids []string
	for candidate := 0; len(paths) < 512; candidate++ {
		content := fmt.Sprintf("unreachable maintenance probe %d\n", candidate)
		hash := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
		if counts[hash[0]] >= 2 {
			continue
		}
		counts[hash[0]]++
		oid := fmt.Sprintf("%x", hash)
		path := filepath.Join(inputs, oid)
		write(t, path, content)
		paths = append(paths, path)
		oids = append(oids, oid)
	}
	args := append([]string{"--git-dir=" + gitDir, "hash-object", "-w", "--no-filters"}, paths...)
	if got := strings.Fields(git(t, args...)); strings.Join(got, " ") != strings.Join(oids, " ") {
		t.Fatal("real Git did not create the expected 512 loose objects")
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, oid := range oids {
		path := filepath.Join(gitDir, "objects", oid[:2], oid[2:])
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	stats := git(t, "--git-dir="+gitDir, "count-objects", "-v")
	first, _, _ := strings.Cut(stats, "\n")
	count, err := strconv.Atoi(strings.TrimPrefix(first, "count: "))
	if err != nil || count < 512 {
		t.Fatalf("loose-object setup was not present: %s, %v", stats, err)
	}
}

func automaticMaintenanceStarted(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var exited, started bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid Git Trace2 setup: %v", err)
		}
		exited = exited || event.Event == "exit"
		args := " " + strings.Join(event.Argv, " ") + " "
		if event.Event == "child_start" && strings.Contains(args, " --auto ") &&
			(strings.Contains(args, " maintenance ") || strings.Contains(args, " gc ")) {
			started = true
		}
	}
	if !exited {
		t.Fatal("Git Trace2 setup recorded no completed command")
	}
	return started
}

func realGitWriter(t *testing.T, pid int) (int, string) {
	t.Helper()
	for range 4 {
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
		if err != nil {
			t.Fatalf("cannot inspect gated writer pid=%d: %v", pid, err)
		}
		args := strings.TrimSpace(string(out))
		if strings.Contains(args, "git") && (strings.Contains(args, "prune") || strings.Contains(args, "pack-objects")) {
			return pid, args
		}
		// A shell can remain as the hook's parent when its executable path
		// needs quoting. Follow only this measured parent chain, never a scan.
		out, err = exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=").Output()
		if err != nil {
			t.Fatal(err)
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil || pid <= 1 {
			t.Fatalf("hook did not descend from a real Git writer: %q, %v", out, err)
		}
	}
	t.Fatal("hook did not descend from a real Git prune/pack-objects writer")
	return 0, ""
}

func (p fixtureMaintenanceProbe) reports(t *testing.T) []fixtureGCReport {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(p.control, "hook-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reports []fixtureGCReport
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var report fixtureGCReport
		if err := json.Unmarshal(data, &report); err != nil || report.Parent <= 1 {
			t.Fatalf("invalid native hook report: %s, %v", data, err)
		}
		reports = append(reports, report)
	}
	return reports
}

func (p fixtureMaintenanceProbe) wait(t *testing.T, ready func() bool, description string) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for !ready() {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		case <-tick.C:
		}
	}
}

func (p fixtureMaintenanceProbe) finish(t *testing.T) {
	t.Helper()
	write(t, filepath.Join(p.control, "release"), "release native GC hook\n")
	p.wait(t, func() bool {
		for _, dir := range p.gitDirs {
			for _, path := range []string{filepath.Join(dir, "gc.pid"), filepath.Join(dir, "objects", "maintenance.lock")} {
				if _, err := os.Stat(path); err == nil {
					return false
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("cannot observe real Git maintenance completion: %v", err)
				}
			}
		}
		for _, report := range p.reports(t) {
			if err := syscall.Kill(report.Parent, 0); !errors.Is(err, syscall.ESRCH) {
				return false
			}
		}
		return true
	}, "real Git to release locks and reap every gated hook's parent before TempDir cleanup")
}
