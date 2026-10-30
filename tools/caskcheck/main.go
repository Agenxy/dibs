// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Command caskcheck loads the generated cask through Homebrew's real DSL and
// step runner. Commands are recorded, never executed: no cask is installed and
// no native service can be registered by this check.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const inspectCask = `
require "cask/cask_loader"
require "simulate_system"
require "install_steps"
require "json"
class DibsRecordingCommand < SystemCommand
  class << self
    attr_accessor :calls
    def run(command, **options)
      self.calls << {command: command.to_s, args: options.fetch(:args, []).map(&:to_s),
                     must_succeed: options.fetch(:must_succeed, false),
                     print_stderr: options.fetch(:print_stderr, false)}
      nil
    end
    def run!(*arguments, **options)
      raise "unexpected command route: #{arguments.inspect}"
    end
  end
end
observations = [[:macos, :arm], [:linux, :arm], [:linux, :intel]].map do |os, arch|
  Homebrew::SimulateSystem.with(os: os, arch: arch) do
    cask = Cask::CaskLoader.load(Pathname(ARGV.fetch(0)))
    DibsRecordingCommand.calls = []
    legacy = cask.artifacts.any? { |a| a.is_a?(Cask::Artifact::AbstractFlightBlock) }
    cask.artifacts.grep(Cask::Artifact::AbstractInstallSteps).each do |artifact|
      artifact.steps.each do |step|
        raise "unsafe inspection step" unless step["type"] == "run" &&
          !step.key?("stdin_path") && !step.key?("stdout_path")
      end
      Homebrew::InstallSteps::Runner.new(context: cask, command: DibsRecordingCommand).run(artifact.steps)
    end
    {os: os.to_s, arch: arch.to_s, legacy: legacy, artifacts: cask.artifacts_list,
     staged_path: cask.staged_path.to_s, app_dir: cask.config.appdir.to_s,
     caveats: cask.caveats.to_s, commands: DibsRecordingCommand.calls}
  end
end
puts JSON.generate(observations)
`

type command struct {
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	MustSucceed bool     `json:"must_succeed"`
	PrintStderr bool     `json:"print_stderr"`
}

type observation struct {
	OS         string                       `json:"os"`
	Arch       string                       `json:"arch"`
	Legacy     bool                         `json:"legacy"`
	Artifacts  []map[string]json.RawMessage `json:"artifacts"`
	StagedPath string                       `json:"staged_path"`
	AppDir     string                       `json:"app_dir"`
	Caveats    string                       `json:"caveats"`
	Commands   []command                    `json:"commands"`
}

func main() {
	path := flag.String("cask", "dist/homebrew/Casks/dibs.rb", "actual generated cask to inspect (no install)")
	flag.Parse()
	if err := run(*path); err != nil {
		fmt.Fprintln(os.Stderr, "caskcheck:", err)
		os.Exit(1)
	}
}

func run(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("generated cask unavailable: %w; build it with the archive gate first", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// #nosec G204 -- fixed Homebrew interpreter and Ruby program; the argument is the generated local cask path
	cmd := exec.CommandContext(ctx, "brew", "ruby", "-e", inspectCask, "--", abs)
	// Per-command developer mode avoids `brew ruby` enabling it persistently.
	// The loader and recorded commands need neither network nor credentials.
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_DEVELOPER=1",
		"HOMEBREW_NO_GITHUB_API=1", "HOMEBREW_NO_ANALYTICS=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("homebrew loader/step inspection failed: %w: %s; use Homebrew 7 after `brew update`",
			err, stderr.String())
	}
	var views []observation
	if err := json.Unmarshal(data, &views); err != nil {
		return fmt.Errorf("homebrew inspection did not return JSON: %w: %s", err, data)
	}
	if err := validate(views); err != nil {
		return err
	}
	if strings.Contains(stderr.String(), "deprecated") {
		return fmt.Errorf("generated cask emits a deprecation: %s; fix the generator configuration", stderr.String())
	}
	fmt.Printf("caskcheck: %d platform views passed the real Homebrew loader and step guards; commands recorded only\n",
		len(views))
	return nil
}

func validate(views []observation) error {
	if len(views) != 3 {
		return fmt.Errorf("expected three release-platform views, got %d", len(views))
	}
	seen := map[string]bool{}
	for _, view := range views {
		key := view.OS + "/" + view.Arch
		if seen[key] || (key != "macos/arm" && key != "linux/arm" && key != "linux/intel") {
			return fmt.Errorf("unexpected or duplicate platform %s", key)
		}
		seen[key] = true
		if err := validateView(view); err != nil {
			return fmt.Errorf("%s: %w; fix .goreleaser.yml, never the generated cask", key, err)
		}
	}
	return nil
}

func validateView(view observation) error {
	if view.Legacy {
		return fmt.Errorf("legacy flight block must be structured postflight_steps")
	}
	if err := validateArtifacts(view); err != nil {
		return err
	}
	if err := validateCommands(view); err != nil {
		return err
	}
	for _, need := range []string{"dibs configure --service", "brew upgrade dibs", "dibs upgrade"} {
		if !strings.Contains(view.Caveats, need) {
			return fmt.Errorf("caveats omit %q", need)
		}
	}
	if view.OS == "linux" {
		for _, need := range []string{"systemctl --user", "notify-send", "libnotify"} {
			if !strings.Contains(view.Caveats, need) {
				return fmt.Errorf("linux caveats omit %q", need)
			}
		}
		for _, foreign := range []string{"Touch ID", "System Settings", "xattr"} {
			if strings.Contains(view.Caveats, foreign) {
				return fmt.Errorf("linux caveats show macOS advice %q", foreign)
			}
		}
	} else if !strings.Contains(view.Caveats, "Touch ID") {
		return fmt.Errorf("macOS caveats omit Touch ID")
	}
	return nil
}

func validateArtifacts(view observation) error {
	counts, err := countArtifacts(view.Artifacts)
	if err != nil {
		return err
	}
	apps := counts["app:Dibs.app"]
	binaries := map[string]int{}
	for _, name := range []string{"dibs", "dibd", "dibs-presence"} {
		binaries[name] = counts["binary:"+name]
	}

	for _, binary := range []string{"dibs", "dibd"} {
		if binaries[binary] != 1 {
			return fmt.Errorf("expected one %s binary, got %d", binary, binaries[binary])
		}
	}
	if view.OS == "linux" {
		if apps != 0 || binaries["dibs-presence"] != 0 {
			return fmt.Errorf("linux selected %d macOS apps and %d presence helpers", apps, binaries["dibs-presence"])
		}
	} else if apps != 1 || binaries["dibs-presence"] != 1 {
		return fmt.Errorf("macOS app or presence helper missing")
	}
	return nil
}

func validateCommands(view observation) error {
	if view.OS == "linux" {
		if len(view.Commands) != 0 {
			return fmt.Errorf("linux selected %d flight commands; "+
				"no service or quarantine command belongs here", len(view.Commands))
		}
		return nil
	}
	if len(view.Commands) != 2 {
		return fmt.Errorf("expected exactly two macOS quarantine commands, got %d", len(view.Commands))
	}
	targets := []string{view.StagedPath, view.AppDir + "/Dibs.app"}
	for i, call := range view.Commands {
		want := []string{"-dr", "com.apple.quarantine", targets[i]}
		if call.Command != "/usr/bin/xattr" || !slices.Equal(call.Args, want) || call.MustSucceed {
			return fmt.Errorf("unexpected or fatal macOS step: %+v", call)
		}
	}
	return nil
}

func artifactName(raw json.RawMessage) (string, error) {
	var args []json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil || len(args) == 0 {
		return "", fmt.Errorf("unreadable artifact arguments %s", raw)
	}
	var name string
	if err := json.Unmarshal(args[0], &name); err != nil {
		return "", fmt.Errorf("unreadable artifact name: %w", err)
	}
	return name, nil
}

func countArtifacts(artifacts []map[string]json.RawMessage) (map[string]int, error) {
	counts := map[string]int{}
	for _, artifact := range artifacts {
		for _, kind := range []string{"app", "binary"} {
			raw, present := artifact[kind]
			if !present {
				continue
			}
			name, err := artifactName(raw)
			if err != nil {
				return nil, err
			}
			if kind == "app" && name != "Dibs.app" {
				return nil, fmt.Errorf("unexpected app %q", name)
			}
			counts[kind+":"+name]++
		}
	}
	return counts, nil
}
