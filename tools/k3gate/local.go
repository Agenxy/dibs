// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultPoll    = time.Minute
	defaultTimeout = 3 * time.Hour
	gatesDir       = "gates"
	bareRepo       = gatesDir + "/dibs.git"
)

type options struct {
	sha, host     string
	poll, timeout time.Duration
}

func runLocal(o options) (int, error) {
	sha, err := output("git", "rev-parse", "--verify", o.sha+"^{commit}")
	if err != nil {
		return 2, fmt.Errorf("resolve %s: %w", o.sha, err)
	}
	short := sha[:10]
	bin, err := crossBuild()
	if err != nil {
		return 2, err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(bin)) }()
	remoteBin := gatesDir + "/bin/k3gate-" + short
	prepare := "mkdir -p " + gatesDir + "/bin && { test -d " + bareRepo + " || git init -q --bare " + bareRepo + "; }"
	steps := [][]string{
		{"ssh", o.host, prepare},
		{"scp", "-q", bin, o.host + ":" + remoteBin},
		{"git", "push", "-q", "--force", o.host + ":" + bareRepo, sha + ":refs/heads/gate/" + short},
		// setsid -f detaches the run from this ssh session, so a dropped
		// connection cannot take the gate down with it.
		{"ssh", o.host, "setsid -f " + remoteBin + " -remote -sha " + sha + " </dev/null >/dev/null 2>&1"},
	}
	for _, s := range steps {
		if err := run(s...); err != nil {
			return 2, err
		}
	}
	fmt.Printf("k3gate: %s started on %s; polling every %s\n", short, o.host, o.poll)
	r, err := await(o, sha)
	if err != nil {
		return 2, err
	}
	report(r)
	return r.ExitCode, nil
}

// await polls until a complete result for sha exists. An ssh failure is
// "not yet": only the remote file can say the gate passed.
func await(o options, sha string) (result, error) {
	path := gatesDir + "/dibs-" + sha[:10] + "/result.json"
	deadline := time.Now().Add(o.timeout)
	for {
		//nolint:gosec // G204: host and path come from the operator's own flags
		b, err := exec.Command("ssh", o.host, "cat "+path+" 2>/dev/null || true").Output()
		if err == nil {
			r, perr := parseResult(b, sha)
			if perr == nil {
				return r, nil
			}
			if !errors.Is(perr, errNoResult) {
				return r, perr
			}
		}
		if time.Now().After(deadline) {
			return result{}, fmt.Errorf("no result for %s after %s", sha[:10], o.timeout)
		}
		time.Sleep(o.poll)
	}
}

func report(r result) {
	for _, s := range r.Steps {
		fmt.Printf("  %-6s exit %d  %6.0fs\n", s.Name, s.ExitCode, s.Seconds)
	}
	fmt.Printf("k3gate: %s exit %d, %d packages passed, %d failed\n",
		r.Head[:10], r.ExitCode, r.PassedPackages, len(r.FailedPackages))
	for _, p := range r.FailedPackages {
		fmt.Println("  FAIL", p)
	}
	if r.Error != "" {
		fmt.Println("  error:", r.Error)
	}
}

// crossBuild compiles this program for the seat, from this checkout.
func crossBuild() (string, error) {
	dir, err := os.MkdirTemp("", "k3gate-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "k3gate")
	//nolint:gosec // G204: a fixed go build of this tool
	cmd := exec.Command("go", "build", "-o", bin, "./tools/k3gate")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=riscv64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("cross-build: %w\n%s", err, out)
	}
	return bin, nil
}

func run(args ...string) error {
	//nolint:gosec // G204: argv built here from constants and the operator's flags
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(args[:min(len(args), 2)], " "), err)
	}
	return nil
}

func output(args ...string) (string, error) {
	var stderr bytes.Buffer
	//nolint:gosec // G204: argv built here from constants and the operator's flags
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
