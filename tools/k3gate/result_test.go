// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"reflect"
	"testing"
)

const head = "dd864291f27a53767c34c92e0ca285f94e07af26"

// The laptop side trusts only the remote file, so the parser is the gate's
// verdict. Each case is a way a pass could be forged by accident: an empty
// poll, a stale run for another commit, a zero exit with nothing behind it.
func TestOnlyACompleteResultForThisCommitCanPass(t *testing.T) {
	ok := `{"head":"` + head + `","exit_code":0,"steps":[{"name":"test","exit_code":0}],"passed_packages":63}`
	if r, err := parseResult([]byte(ok), head); err != nil || r.ExitCode != 0 {
		t.Fatalf("a real pass was refused: %v %+v", err, r)
	}
	for name, body := range map[string]string{
		"stale":       `{"head":"0000000000000000000000000000000000000000","exit_code":0,"steps":[{"name":"test"}]}`,
		"no steps":    `{"head":"` + head + `","exit_code":0}`,
		"failed step": `{"head":"` + head + `","exit_code":0,"steps":[{"name":"vet","exit_code":1}]}`,
		"failed pkg":  `{"head":"` + head + `","exit_code":0,"steps":[{"name":"test"}],"failed_packages":["x/y"]}`,
		"truncated":   `{"head":"` + head + `","exit_co`,
	} {
		if _, err := parseResult([]byte(body), head); err == nil {
			t.Errorf("%s: forged pass accepted", name)
		}
	}
	if _, err := parseResult([]byte("  \n"), head); !errors.Is(err, errNoResult) {
		t.Errorf("an empty poll must read as not-yet, got %v", err)
	}
	red := `{"head":"` + head + `","exit_code":1,"steps":[{"name":"test","exit_code":1}],"failed_packages":["a/b"]}`
	if r, err := parseResult([]byte(red), head); err != nil || r.ExitCode != 1 {
		t.Fatalf("a real failure must come back as a failure: %v %+v", err, r)
	}
}

func TestPackagesCountsEachPackageOnce(t *testing.T) {
	out := "ok  \tgithub.com/agenxy/dibs/internal/core\t1.2s\n" +
		"--- FAIL: TestX (0.1s)\nFAIL\nFAIL\tgithub.com/agenxy/dibs/internal/mcp\t5.8s\n" +
		"ok  \tgithub.com/agenxy/dibs/internal/ledger\t(cached)\n" +
		"FAIL\tgithub.com/agenxy/dibs/cmd/dibs [build failed]\n"
	passed, failed := packages([]byte(out))
	want := []string{"github.com/agenxy/dibs/cmd/dibs", "github.com/agenxy/dibs/internal/mcp"}
	if passed != 2 || !reflect.DeepEqual(failed, want) {
		t.Fatalf("passed=%d failed=%v", passed, failed)
	}
}
