// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// result is what the remote half writes and the laptop half trusts. ExitCode
// is the only verdict: zero exactly when every step exited zero.
type result struct {
	Head           string   `json:"head"`
	ExitCode       int      `json:"exit_code"`
	Steps          []step   `json:"steps"`
	PassedPackages int      `json:"passed_packages"`
	FailedPackages []string `json:"failed_packages"`
	Error          string   `json:"error,omitempty"`
}

type step struct {
	Name     string  `json:"name"`
	ExitCode int     `json:"exit_code"`
	Seconds  float64 `json:"seconds"`
}

// packages reads `go test` output. A package counts once: "FAIL\t<pkg>"
// marks a failure, and "ok  \t<pkg>" a pass.
func packages(out []byte) (passed int, failed []string) {
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || seen[fields[1]] {
			continue
		}
		switch fields[0] {
		case "ok":
			seen[fields[1]] = true
			passed++
		case "FAIL":
			if strings.Contains(fields[1], "/") {
				seen[fields[1]] = true
				failed = append(failed, fields[1])
			}
		}
	}
	sort.Strings(failed)
	return passed, failed
}

// parseResult refuses anything that is not a complete result for this
// commit, so a truncated read or a stale file can never pass the gate.
func parseResult(b []byte, head string) (result, error) {
	var r result
	if len(bytes.TrimSpace(b)) == 0 {
		return r, errNoResult
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("unreadable result: %w", err)
	}
	if r.Head != head {
		return r, fmt.Errorf("result is for %q, not %q", r.Head, head)
	}
	if r.ExitCode == 0 && (len(r.Steps) == 0 || len(r.FailedPackages) > 0) {
		return r, errors.New("result claims success without the steps that earn it")
	}
	for _, s := range r.Steps {
		if r.ExitCode == 0 && s.ExitCode != 0 {
			return r, fmt.Errorf("result claims success but step %s exited %d", s.Name, s.ExitCode)
		}
	}
	return r, nil
}

// writeResult replaces result.json in one rename, so a poll never reads half.
func writeResult(dir string, r result) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ".result.json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "result.json"))
}
