// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefuseUnsafeOrUnboundedModesBeforeWriting(t *testing.T) {
	for _, change := range []func(*options){
		func(o *options) { o.tag = "main" }, func(o *options) { o.mode = "mac-cask" },
		func(o *options) { o.fresh = 0 }, func(o *options) { o.fresh = 21 },
		func(o *options) { o.runtime = "sh" }, func(o *options) { o.mode = "linux-child" },
	} {
		o := options{mode: "native", tag: "v0.0.17", out: filepath.Join(t.TempDir(), "new"), runtime: "container", fresh: 5}
		change(&o)
		if err := execute(context.Background(), o); err == nil {
			t.Fatalf("unsafe mode accepted: %+v", o)
		}
		if _, err := os.Stat(o.out); !os.IsNotExist(err) {
			t.Fatal("refusal wrote to output root")
		}
	}
}

func TestExistingReceiptDirectoryIsNeverReused(t *testing.T) {
	root := t.TempDir()
	o := options{mode: "native", tag: "v0.0.17", out: root, runtime: "container", fresh: 5}
	if err := execute(context.Background(), o); err == nil {
		t.Fatal("existing directory accepted")
	}
}

func TestHostedLinuxModeRequiresAnEphemeralRunnerAndOneAttempt(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	o := options{mode: "linux-hosted", tag: "v0.0.16", out: "new-receipt", runtime: "container", fresh: 1}
	if err := validate(o, "linux"); err != nil {
		t.Fatal("hosted setup refused:", err)
	}
	if err := validate(o, "darwin"); err == nil {
		t.Fatal("hosted mode accepted a Mac")
	}
	o.fresh = 2
	if err := validate(o, "linux"); err == nil {
		t.Fatal("one runner was counted as multiple fresh installations")
	}
	o.fresh = 1
	t.Setenv("RUNNER_ENVIRONMENT", "self-hosted")
	if err := validate(o, "linux"); err == nil {
		t.Fatal("hosted mode accepted a persistent runner")
	}
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	t.Setenv("GITHUB_ACTIONS", "")
	if err := validate(o, "linux"); err == nil {
		t.Fatal("hosted mode accepted an ordinary workstation")
	}
}

func TestTagFenceRejectsDifferentOrAmbiguousCasks(t *testing.T) {
	for _, body := range []string{
		`{"casks":[]}`, `{"casks":[{"token":"dibs","version":"0.0.16"}]}`,
		`{"casks":[{"token":"other","version":"0.0.17"}]}`, `not-json`,
	} {
		if err := caskVersion(body, "v0.0.17"); err == nil {
			t.Fatalf("wrong public cask accepted: %s", body)
		}
	}
	if err := caskVersion(`{"casks":[{"token":"dibs","version":"0.0.17"}]}`, "v0.0.17"); err != nil {
		t.Fatal(err)
	}
}

func TestChildEnvironmentCannotReachExistingHarnessOrBoard(t *testing.T) {
	for key, value := range map[string]string{
		"DIBS_TOKEN": "private-token", "DIBS_ADDR": "production.invalid:4777",
		"CODEX_THREAD_ID": "live-thread", "CLAUDE_CODE_SESSION_ID": "live-session", "CODEX_HOME": "/production/codex",
		"HOMEBREW_NO_GITHUB_API": "1", "GOPROXY": "https://private:credential@example.invalid",
	} {
		t.Setenv(key, value)
	}
	env := strings.Join(isolatedEnv("/private-fixture/home", "/private-fixture"), "\n")
	for _, forbidden := range []string{"private-token", "production.invalid", "live-thread", "live-session", "/production/codex", "HOMEBREW_NO_GITHUB_API=", "credential@"} {
		if strings.Contains(env, forbidden) {
			t.Fatalf("private/live state inherited: %s", forbidden)
		}
	}
	for _, required := range []string{"HOME=/private-fixture/home", "DIBS_ADDR=127.0.0.1:1", "DIBS_NOTIFY=off", "GOBIN=/private-fixture/home/bin"} {
		if !strings.Contains(env, required) {
			t.Fatalf("missing isolation: %s", required)
		}
	}
}

func TestMCPSetupFailuresCannotBecomeSuccessfulRoundTrips(t *testing.T) {
	for _, broken := range []string{
		"", "register", "check_in", "declare", "send", "inbox", "respond", "read_mail", "collision",
	} {
		t.Run(broken, func(t *testing.T) {
			calls := map[string]int{}
			call := func(name string, args map[string]any) (map[string]any, error) {
				calls[name]++
				if name == broken {
					return nil, errors.New("injected actual step refusal")
				}
				switch name {
				case "register":
					return map[string]any{"token": "fixture-only"}, nil
				case "declare":
					if broken == "collision" {
						return map[string]any{"slot_id": "s1"}, nil
					}
					return map[string]any{"slot_id": "s1", "overlaps": []any{map[string]any{"agent": "surface-a-0"}}}, nil
				case "send":
					return map[string]any{"msg_serial": float64(7)}, nil
				case "inbox":
					return map[string]any{"body": "Are you checking this installation?"}, nil
				case "read_mail":
					return map[string]any{"response": "Yes, installation checked."}, nil
				default:
					return map[string]any{"ok": true}, nil
				}
			}
			err := coordination(call, t.TempDir(), 0)
			if broken == "" {
				if err != nil || calls["read_mail"] != 1 {
					t.Fatalf("positive round trip failed: %v", err)
				}
				return
			}
			if err == nil || (broken != "collision" && calls[broken] == 0) {
				t.Fatal("setup refusal did not fail actual coordination path")
			}
		})
	}
}

func TestImageRunUsesObservedDigest(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, raw := range []string{
		`[{"RepoDigests":["homebrew/brew@` + digest + `"]}]`,
		`[{"configuration":{"descriptor":{"digest":"` + digest + `"}}}]`,
	} {
		image, err := resolvedImage("docker.io/homebrew/brew:latest", raw)
		if err != nil || !strings.Contains(image, "@"+digest) {
			t.Fatalf("image not bound to observed content: %s %v", image, err)
		}
	}
	if _, err := resolvedImage("latest", "[{}]"); err == nil {
		t.Fatal("unresolved mutable image accepted")
	}
	if _, err := resolvedImage("latest", `[{"RepoDigests":["homebrew/brew:latest"]}]`); err == nil {
		t.Fatal("runtime reference without a digest accepted")
	}
}

func TestCommandUsesExitStatusRatherThanSuccessText(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{ctx: context.Background(), root: t.TempDir()}
	env := append(os.Environ(), "DIBS_PUBLIC_COMMAND_FIXTURE=1")
	_, err = r.command("failure-control", env, exe, "-test.run=^TestPublicCommandExitFixture$")
	if err == nil || len(r.report.Steps) != 1 || r.report.Steps[0].Exit != 7 {
		t.Fatalf("successful prose hid command failure: %v %+v", err, r.report.Steps)
	}
}

func TestPublicCommandExitFixture(_ *testing.T) {
	if os.Getenv("DIBS_PUBLIC_COMMAND_FIXTURE") != "1" {
		return
	}
	fmt.Println("all checks passed")
	os.Exit(7)
}

func TestCommandKeepsJSONOnStdoutAndWarningsInTheLog(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{ctx: context.Background(), root: t.TempDir()}
	env := append(os.Environ(), "DIBS_PUBLIC_JSON_FIXTURE=1")
	out, err := r.command("brew-tag", env, exe, "-test.run=^TestPublicCommandJSONFixture$")
	if err != nil || caskVersion(out, "v0.0.16") != nil {
		t.Fatalf("successful structured stdout was polluted by stderr: %v %q", err, out)
	}
	log, err := os.ReadFile(filepath.Join(r.root, r.report.Steps[0].Log))
	if err != nil || !strings.Contains(string(log), "Warning: Calling `postflight`") ||
		!strings.Contains(string(log), `"version":"0.0.16"`) {
		t.Fatalf("command evidence lost stdout or stderr: %v %s", err, log)
	}
	if len(r.report.Findings) != 1 || r.report.Findings[0].Step != "brew-tag" ||
		!strings.Contains(r.report.Findings[0].FixedBy, "#519") {
		t.Fatalf("public warning was not recorded with its known source repair: %+v", r.report.Findings)
	}
}

func TestPublicCommandJSONFixture(_ *testing.T) {
	if os.Getenv("DIBS_PUBLIC_JSON_FIXTURE") != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "Warning: Calling `postflight` is deprecated! Use `postflight_steps` instead.")
	fmt.Print(`{"casks":[{"token":"dibs","version":"0.0.16"}]}`)
	os.Exit(0)
}
