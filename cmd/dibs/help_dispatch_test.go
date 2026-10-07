// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommandCatalogContainsEveryDispatchAlias(t *testing.T) {
	verbs := dispatchedVerbs(t)
	if !slices.Contains(verbs, "get") {
		t.Fatal("dispatch inventory omitted a second case alias")
	}
	for _, verb := range verbs {
		if !slices.Contains(commands, verb) {
			t.Errorf("dispatched verb %q missing from command catalog", verb)
		}
	}
	for _, verb := range commands {
		if !slices.Contains(verbs, verb) {
			t.Errorf("catalog verb %q is not dispatched", verb)
		}
	}
}

// Enter through main, not an individual handler: help has previously been
// correct below the dispatch while the dispatch performed the action instead.
func TestEveryDispatchedVerbAnswersHelpWithoutAuthority(t *testing.T) {
	var requests atomic.Int32
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer board.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range dispatchedVerbs(t) {
		for _, help := range []string{"--help", "-h"} {
			t.Run(verb+"/"+help, func(t *testing.T) {
				before := requests.Load()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, exe, "-test.run=^TestCLIHelpProcess$", "--", verb, help)
				cmd.Dir = t.TempDir()
				// Never inherit board credentials, harness/session/socket hints,
				// or the operator's data directory. Only a refusing fixture is
				// reachable, and even contacting it violates the help contract.
				cmd.Env = []string{
					"DIBS_TEST_CLI_HELP=1", "DIBS_ADDR=" + strings.TrimPrefix(board.URL, "http://"),
					"DIBS_DIR=" + cmd.Dir,
				}
				for _, key := range []string{"PATH", "TMPDIR", "TEMP", "SystemRoot", "SYSTEMROOT"} {
					if value, ok := os.LookupEnv(key); ok {
						cmd.Env = append(cmd.Env, key+"="+value)
					}
				}
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if err := cmd.Run(); err != nil {
					t.Fatalf("help failed: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
				if !strings.HasPrefix(stdout.String(), "usage: dibs "+verb) {
					t.Errorf("missing command usage on stdout: %q", stdout.String())
				}
				firstLine, _, _ := strings.Cut(stdout.String(), "\n")
				if verb == "put" && !strings.Contains(firstLine, "<file>") {
					t.Error("put help does not name its input file")
				}
				if verb == "get" {
					if !strings.Contains(firstLine, "<blob>") || !strings.Contains(firstLine, "<path>") {
						t.Error("get help does not name its blob and destination")
					}
					if strings.Contains(stdout.String(), "-mime") {
						t.Error("get help advertises a put-only MIME flag")
					}
				}
				if stderr.Len() != 0 {
					t.Errorf("help wrote a diagnostic: %q", stderr.String())
				}
				for _, flag := range map[string][]string{
					"invite": {"-ttl", "7d"}, "mcp-config": {"-board"},
					"web": {"-password"}, "identity": {"-cwd"},
					"put": {"-mime", "-json"}, "get": {"-out", "-json"},
				}[verb] {
					if !strings.Contains(stdout.String(), flag) {
						t.Errorf("command help lost flag detail %q", flag)
					}
				}
				if requests.Load() != before {
					t.Error("asking for help contacted the board")
				}
				entries, err := os.ReadDir(cmd.Dir)
				if err != nil || len(entries) != 0 {
					t.Errorf("asking for help changed the data directory: %v %v", entries, err)
				}
			})
		}
	}
}

func TestCLIHelpProcess(t *testing.T) {
	if os.Getenv("DIBS_TEST_CLI_HELP") != "1" {
		return
	}
	marker := slices.Index(os.Args, "--")
	if marker < 0 {
		t.Fatal("missing helper argument separator")
	}
	os.Args = append([]string{"dibs"}, os.Args[marker+1:]...)
	main()
	os.Exit(0)
}
