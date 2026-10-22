// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type pairedProcessEvidence struct {
	PID        int
	Executable string
	Args, Env  []string
}

func TestPairedDaemonExecProcess(t *testing.T) {
	t.Helper()
	if os.Getenv("DIBS_TEST_PAIRED_EXEC") != "1" {
		return
	}
	if err := enterAppDaemon(); err != nil {
		_, _ = os.Stderr.WriteString(err.Error())
		os.Exit(3)
	}
	self, err := os.Executable()
	if err != nil {
		os.Exit(4)
	}
	if err := json.NewEncoder(os.Stdout).Encode(pairedProcessEvidence{os.Getpid(), self, os.Args, os.Environ()}); err != nil {
		os.Exit(5)
	}
	os.Exit(0)
}

// Run real copied Mach-O images through the production entry function. A
// missing location guard loops until the context kills it, rather than passing
// because a mocked exec returned. No daemon or macOS service is started.
func TestPairedDaemonExecPreservesProcessAndRefusesStaleCode(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "dibd")
	paired := filepath.Join(dir, "Dibs.app", "Contents", "MacOS", "dibd")
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paired), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, at := range []string{root, paired} {
		if err := os.WriteFile(at, image, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Re-signing the nested image changes its signature while its sections
	// stay identical: comparison of whole file bytes would refuse this case.
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--identifier", "org.agenxy.dibs.daemon", paired).CombinedOutput(); err != nil {
		t.Fatalf("sign nested setup: %v %s", err, out)
	}
	run := func() (pairedProcessEvidence, int, []byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, root, "-test.run=^TestPairedDaemonExecProcess$", "--", "literal space", "$(not-a-shell)")
		cmd.Args[0] = "original argv zero"
		cmd.Env = append(os.Environ(), "DIBS_TEST_PAIRED_EXEC=1", "DIBS_PAIRED_ENV=literal space=$value")
		out, err := cmd.Output()
		if err != nil {
			var e *exec.ExitError
			if errors.As(err, &e) {
				out = append(out, e.Stderr...)
			}
			return pairedProcessEvidence{}, 0, out, err
		}
		var got pairedProcessEvidence
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("child evidence: %v %s", err, out)
		}
		if !reflect.DeepEqual(got.Args, cmd.Args) || !reflect.DeepEqual(got.Env, cmd.Env) {
			t.Fatalf("argv/environment changed across exec: %+v", got)
		}
		return got, cmd.Process.Pid, out, nil
	}
	got, pid, out, err := run()
	if err != nil {
		t.Fatalf("positive paired exec: %v %s", err, out)
	}
	want, err := filepath.EvalSymlinks(paired)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != pid || got.Executable != want {
		t.Fatalf("not a same-PID paired exec: got %+v launched PID %d want %s", got, pid, want)
	}
	other, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paired); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paired, other, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, out, err := run(); err == nil || !strings.Contains(string(out), "refusing mismatched paired daemon") || !strings.Contains(string(out), "sections") {
		t.Fatalf("stale pair not diagnosed: %v %s", err, out)
	}
	if err := os.Remove(paired); err != nil {
		t.Fatal(err)
	}
	if _, _, out, err := run(); err == nil || !strings.Contains(string(out), "rebuild or reinstall the complete Dibs distribution") {
		t.Fatalf("incomplete paired app was accepted: %v %s", err, out)
	}
	if err := os.RemoveAll(filepath.Join(dir, "Dibs.app")); err != nil {
		t.Fatal(err)
	}
	got, pid, out, err = run()
	if err != nil || got.PID != pid {
		t.Fatalf("standalone control: %v %s", err, out)
	}
}

// The copied-image test above measures exec itself. This one enters main in
// a production-built daemon, so removing the call from main cannot leave the
// entire contract green. It only prints a manual: no board or service starts.
func TestProductionDaemonMainEntersItsPairedApp(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "dibd")
	build := exec.Command("go", "build", "-o", root, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual daemon: %v %s", err, out)
	}
	paired := filepath.Join(dir, "Dibs.app", "Contents", "MacOS", "dibd")
	if err := os.MkdirAll(filepath.Dir(paired), 0o700); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paired, image, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, root, "-man").CombinedOutput()
	}
	if out, err := run(); err != nil || !strings.Contains(string(out), ".Dt DIBD 8") {
		t.Fatalf("positive actual-main control: %v %s", err, out)
	}
	other, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paired); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paired, other, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err == nil || !strings.Contains(string(out), "refusing mismatched paired daemon") {
		t.Fatalf("actual daemon main did not refuse stale paired code: %v %s", err, out)
	}
	// A deliberately differently named development build is not the archive's
	// root entry point. It must remain usable beside the production app without
	// changing the refusal above for the real root name.
	development := filepath.Join(dir, "dibd-dev")
	if err := os.WriteFile(development, image, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, development, "-man").CombinedOutput(); err != nil || !strings.Contains(string(out), ".Dt DIBD 8") {
		t.Fatalf("differently named development image did not stay standalone: %v %s", err, out)
	}
}
