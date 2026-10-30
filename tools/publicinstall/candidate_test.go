// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateCaskRetainsEveryByteExceptURLAuthority(t *testing.T) {
	original := []byte(`cask "dibs" do
  version "0.0.17"
  homepage "https://github.com/agenxy/dibs"
  sha256 "one"
  url "https://github.com/Agenxy/dibs/releases/download/v0.0.17/dibs_#{version}_darwin_arm64.tar.gz"
  sha256 "two"
  url "https://github.com/Agenxy/dibs/releases/download/v0.0.17/dibs_#{version}_linux_arm64.tar.gz"
  sha256 "three"
  url "https://github.com/Agenxy/dibs/releases/download/v0.0.17/dibs_#{version}_linux_amd64.tar.gz"
  binary "dibs"
  binary "dibd"
  caveats "configure --service"
  postflight_steps do
    on_macos do
      run "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "{{staged_path}}"], must_succeed: false
    end
  end
end
`)
	const origin = "http://127.0.0.1:12345"
	got, err := adaptCandidateCask(original, "v0.0.17", origin)
	if err != nil {
		t.Fatal("setup: real cask shape rejected:", err)
	}
	if restored := strings.ReplaceAll(string(got), `url "`+origin, `url "https://github.com`); restored != string(original) {
		t.Fatalf("adapter changed checksums, archive paths, steps, binaries, caveats or homepage:\n%s", got)
	}
	// GoReleaser's real canonical-version snapshot uses this interpolation;
	// ordinary snapshot versions leave the release tag literal instead.
	interpolated := strings.ReplaceAll(string(original), "download/v0.0.17/", "download/v#{version}/")
	got, err = adaptCandidateCask([]byte(interpolated), "v0.0.17", origin)
	if err != nil || strings.ReplaceAll(string(got), `url "`+origin, `url "https://github.com`) != interpolated {
		t.Fatalf("actual canonical cask URL form was rejected or rewritten beyond its authority: %v", err)
	}
	for _, changed := range []string{
		strings.Replace(string(original), "v0.0.17/", "v0.0.16/", 1),
		strings.Replace(string(original), `version "0.0.17"`, `version "0.0.16"`, 1),
		strings.Replace(string(original), "url \"https://github.com/Agenxy", "url \"http://github.com/Agenxy", 1),
		strings.Replace(string(original), "url \"https://github.com/Agenxy", "# url \"https://github.com/Agenxy", 1),
	} {
		if _, err := adaptCandidateCask([]byte(changed), "v0.0.17", origin); err == nil {
			t.Fatal("changed generated download contract was accepted")
		}
	}
}

func TestCandidateManifestRefusesWrongIdentityPathsAndBytes(t *testing.T) {
	for _, mutation := range []string{"none", "sha", "version", "path", "reordered", "extra", "bytes"} {
		t.Run(mutation, func(t *testing.T) {
			dir := t.TempDir()
			o := options{candidateDir: dir, sha: strings.Repeat("a", 40), tag: "v0.0.17"}
			m := candidateManifest{Schema: 1, SHA: o.sha, Version: "0.0.17"}
			for _, name := range candidateNames(m.Version) {
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, []byte("fixture:"+name), 0o600); err != nil {
					t.Fatal(err)
				}
				digest, err := candidateDigest(path)
				if err != nil {
					t.Fatal(err)
				}
				m.Files = append(m.Files, candidateFile{name, digest})
			}
			switch mutation {
			case "sha":
				m.SHA = strings.Repeat("b", 40)
			case "version":
				m.Version = "0.0.16"
			case "path":
				m.Files[0].Name = "../dibs.rb"
			case "reordered":
				m.Files[0], m.Files[1] = m.Files[1], m.Files[0]
			case "extra":
				m.Files = append(m.Files, m.Files[0])
			case "bytes":
				if err := os.WriteFile(filepath.Join(dir, m.Files[0].Name), []byte("replacement cask"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			body, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "candidate.json"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = readCandidate(o)
			if (err == nil) != (mutation == "none") {
				t.Fatalf("manifest admission mismatch: %v", err)
			}
		})
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("DIBS_TEST_INSTALLED_MODE") == "smoke" && filepath.Base(os.Args[0]) == "dibd" {
		os.Exit(installedDaemonFixture())
	}
	if os.Getenv("DIBS_TEST_INSTALLED_MODE") != "" && filepath.Base(os.Args[0]) == "dibs" {
		os.Exit(installedCommandFixture())
	}
	if os.Getenv("DIBS_TEST_BREW_CASE") != "" && filepath.Base(os.Args[0]) == "brew" {
		os.Exit(candidateBrewFixture())
	}
	os.Exit(m.Run())
}

func candidateBrewFixture() int {
	log := os.Getenv("DIBS_TEST_BREW_LOG")
	previous, _ := os.ReadFile(log)
	f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 99
	}
	_, writeErr := fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
	if err := f.Close(); err != nil || writeErr != nil {
		return 99
	}
	mode := os.Getenv("DIBS_TEST_BREW_CASE")
	switch os.Args[1] {
	case "list":
		if mode == "registration-mismatch" {
			fmt.Println("dibs 0.0.16")
		} else {
			fmt.Println("dibs 0.0.17")
		}
	case "uninstall":
		if mode == "cleanup-failure" {
			return 7
		}
	case "install":
		if mode == "success" || (mode == "recover" && strings.Count(string(previous), "install --cask") > 0) {
			return 0
		}
		message := "Error: agenxy/tap/dibs: Broken pipe"
		switch mode {
		case "other-error":
			message = "Error: agenxy/tap/dibs: checksum mismatch"
		case "other-pipe":
			message = "Error: other/tap/package: Broken pipe"
		}
		fmt.Fprintln(os.Stderr, message)
		return 1
	default:
		return 98
	}
	return 0
}

func TestCandidateRetryIsBoundedAndExclusiveToItsMeasuredHomebrewFailure(t *testing.T) {
	for _, tc := range []struct {
		mode              string
		attempts, pipes   int
		cleanups, recover int
		failed            bool
	}{
		{"success", 1, 0, 0, 0, false},
		{"recover", 2, 1, 1, 1, false},
		{"repeated-pipe", 2, 2, 1, 0, true},
		{"other-error", 1, 0, 0, 0, true},
		{"other-pipe", 1, 0, 0, 0, true},
		{"registration-mismatch", 1, 1, 0, 0, true},
		{"cleanup-failure", 1, 1, 1, 0, true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			r, _ := commandTestFixture(t, "brew")
			root := r.root
			// exec.Command resolves its executable in the parent environment,
			// before cmd.Env is applied. Qualify that exact lookup, not just
			// the environment passed after a real executable was already found.
			t.Setenv("PATH", root)
			resolved, err := exec.LookPath("brew")
			if err != nil || resolved != filepath.Join(root, "brew") {
				t.Fatalf("setup: fixture executable was not selected: %q %v", resolved, err)
			}
			log := filepath.Join(root, "actual-argv.log")
			env := append(os.Environ(), "PATH="+root, "DIBS_TEST_BREW_CASE="+tc.mode, "DIBS_TEST_BREW_LOG="+log)
			r.env = env
			err = r.candidateFirstInstall("v0.0.17")
			commands, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal("setup: fixture did not execute:", readErr)
			}
			if (err != nil) != tc.failed || r.report.Attempts != tc.attempts || r.report.EPIPE != tc.pipes ||
				r.report.Recovered != tc.recover || strings.Count(string(commands), "uninstall --cask") != tc.cleanups {
				t.Fatalf("wrong retry boundary: err=%v report=%+v argv=%s", err, r.report, commands)
			}
		})
	}
}
