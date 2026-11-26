// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// gateSteps is the Go half of `task ci`. The seat has no mise, bun or
// browser, so lint and the e2e suites stay on the Pro and hosted CI.
var gateSteps = []struct {
	name string
	args []string
}{
	{"vet", []string{"go", "vet", "./..."}},
	{"build", []string{"go", "build", "./..."}},
	{"test", []string{"go", "test", "-race", "-p", "2", "-count=1", "-timeout=60m", "./..."}},
	{"vuln", []string{"go", "run", "golang.org/x/vuln/cmd/govulncheck@v1.9.0", "./..."}},
}

func runRemote(sha string) (int, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 2, err
	}
	if len(sha) < 10 {
		return 2, fmt.Errorf("-remote needs a full commit id, got %q", sha)
	}
	gates := filepath.Join(home, gatesDir)
	unlock, err := lock(filepath.Join(gates, "k3gate.lock"))
	if err != nil {
		return 2, err
	}
	defer unlock()
	dir := filepath.Join(gates, "dibs-"+sha[:10])
	if err := os.RemoveAll(dir); err != nil {
		return 2, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 2, err
	}
	r := gate(home, gates, dir, sha)
	if err := writeResult(dir, r); err != nil {
		return 2, err
	}
	return r.ExitCode, nil
}

// gate always returns a result, so a failure before the tests still reaches
// the laptop as a red result rather than as silence.
func gate(home, gates, dir, sha string) result {
	r := result{Head: sha, ExitCode: 2}
	//nolint:gosec // G304: the run directory is fixed under the seat's own home
	log, err := os.Create(filepath.Join(dir, "ci.log"))
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer func() { _ = log.Close() }()
	src := filepath.Join(dir, "source")
	for _, args := range [][]string{
		{"git", "clone", "-q", filepath.Join(gates, "dibs.git"), src},
		{"git", "-C", src, "checkout", "-q", "--detach", sha},
	} {
		//nolint:gosec // G204: git with the seat's own bare repo and the commit being gated
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout, cmd.Stderr = log, log
		if err := cmd.Run(); err != nil {
			r.Error = fmt.Sprintf("%s %s: %v", args[0], args[1], err)
			return r
		}
	}
	r.ExitCode = 0
	for _, s := range gateSteps {
		var captured bytes.Buffer
		started := time.Now()
		code := polite(home, src, io.MultiWriter(log, &captured), s.args)
		r.Steps = append(r.Steps, step{Name: s.name, ExitCode: code, Seconds: time.Since(started).Seconds()})
		if s.name == "test" {
			r.PassedPackages, r.FailedPackages = packages(captured.Bytes())
		}
		if code != 0 && r.ExitCode == 0 {
			r.ExitCode = 1
		}
	}
	return r
}

// polite runs one step at the lowest CPU and IO priority, with the seat's
// own Go and a bounded scheduler.
func polite(home, dir string, out io.Writer, args []string) int {
	argv := append([]string{"-n", "19", "ionice", "-c3"}, args...)
	//nolint:gosec // G204: argv is gateSteps, a constant table
	cmd := exec.Command("nice", argv...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Join(home, "sdk", "go", "bin")+":/usr/local/bin:/usr/bin:/bin",
		"GOTOOLCHAIN=local", "GOMAXPROCS=4")
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return max(exit.ExitCode(), 1)
	default:
		_, _ = fmt.Fprintln(out, "k3gate:", err)
		return 1
	}
}
