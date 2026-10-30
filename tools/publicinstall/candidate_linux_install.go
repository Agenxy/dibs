// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/paths"
)

const candidateTap = "agenxy/tap/dibs"

func (r *runner) candidateBrew(o options, origin string) error {
	tap := filepath.Join(r.root, "fixture-tap")
	if err := os.MkdirAll(filepath.Join(tap, "Casks"), 0o700); err != nil {
		return err
	}
	original, err := os.ReadFile(filepath.Join(o.candidateDir, "dibs.rb"))
	if err != nil {
		return err
	}
	adapted, err := adaptCandidateCask(original, o.tag, origin)
	if err != nil {
		return err
	}
	// #nosec G703 -- fixed cask path beneath this invocation's new private fixture tap
	if err := os.WriteFile(filepath.Join(tap, "Casks", "dibs.rb"), adapted, 0o600); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"git", "-C", tap, "init"},
		{"git", "-C", tap, "add", "Casks/dibs.rb"},
		{
			"git", "-C", tap, "-c", "user.name=Dibs acceptance", "-c", "user.email=acceptance@users.noreply.github.com",
			"commit", "-m", "Generated candidate cask; download origin only",
		},
		{"brew", "update"},
		{"brew", "--version"},
		{"brew", "tap", "agenxy/tap", "file://" + tap},
		{"brew", "trust", "agenxy/tap"},
		{"brew", "install", "go", "cosign"},
	} {
		if _, err := r.command("candidate-brew-setup", r.env, args...); err != nil {
			return err
		}
	}
	info, err := r.command("candidate-cask-version", r.env, "brew", "info", "--json=v2", "--cask", candidateTap)
	if err != nil {
		return err
	}
	if err := caskVersion(info, o.tag); err != nil {
		return err
	}
	if err := r.candidateFirstInstall(o.tag); err != nil {
		return err
	}
	prefix, err := r.command("candidate-brew-prefix", r.env, "brew", "--prefix")
	if err != nil {
		return err
	}
	return r.exerciseSystemdService(filepath.Join(strings.TrimSpace(prefix), "bin"))
}

// This exact cask-scoped Homebrew exception is the measured bug B signature.
// Generic EPIPE, other commands and other casks do not qualify for a retry.
func knownHomebrewEPIPE(out string) bool {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.TrimSpace(line) == "Error: agenxy/tap/dibs: Broken pipe" {
			return true
		}
	}
	return false
}

func (r *runner) candidateFirstInstall(tag string) error {
	for attempt := range 2 {
		r.report.Attempts++
		out, err := r.command("candidate-brew-install", r.env, "brew", "install", "--cask", candidateTap)
		if err == nil {
			if attempt == 1 {
				r.report.Recovered++
			}
			return nil
		}
		r.report.InstallFailed++
		if !knownHomebrewEPIPE(out) {
			return err
		}
		r.report.EPIPE++
		r.report.Findings = append(r.report.Findings, finding{
			Step:    "candidate-brew-install",
			Warning: "Homebrew cask-scoped Broken pipe signature; upstream credential/analytics probe previously measured",
		})
		if attempt == 1 {
			return err // exactly one fresh retry, never an automatic loop.
		}
		listed, listErr := r.command("candidate-failed-registration", r.env, "brew", "list", "--cask", "--versions", "dibs")
		if listErr != nil || strings.TrimSpace(listed) != "dibs "+strings.TrimPrefix(tag, "v") {
			return errors.New("homebrew EPIPE did not leave the exact candidate registered; fresh retry refused")
		}
		_, err = r.command("candidate-fresh-retry-cleanup", r.env, "brew", "uninstall", "--cask", candidateTap)
		if err != nil {
			return err
		}
	}
	return errors.New("candidate installation did not complete")
}

func (r *runner) repeatInstalledConfigure() error {
	unit := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user", "dibs.service")
	if _, err := r.command("candidate-configure-first", r.env, "dibs", "configure", "--service"); err != nil {
		return err
	}
	first, err := os.ReadFile(unit) // #nosec G304 G703 -- fixed unit in the ephemeral runner's isolated home
	if err != nil || !strings.HasPrefix(string(first), "# written by dibs configure --service; sha256 of the rest: ") {
		return errors.New("real candidate configure did not write its managed service receipt")
	}
	if _, err := r.command("candidate-configure-repeat", r.env, "dibs", "configure", "--service"); err != nil {
		return err
	}
	second, err := os.ReadFile(unit) // #nosec G304 G703 -- same fixture unit, byte-for-byte repeat check
	if err != nil || !bytes.Equal(first, second) {
		return errors.New("repeated real candidate configure changed the service bytes")
	}
	return nil
}

type serviceProofLayout struct{ home, prefix, oldBin, newBin string }

func (r *runner) prepareServiceProof(candidate string) (serviceProofLayout, error) {
	var p serviceProofLayout
	old, err := r.oldPublishedBinaries()
	if err != nil {
		return p, err
	}
	p.home, p.prefix = filepath.Join(r.root, "legacy-home"), filepath.Join(r.root, "legacy-prefix")
	p.oldBin = filepath.Join(p.prefix, "Caskroom", "dibs", "0.0.16")
	p.newBin = filepath.Join(p.prefix, "Caskroom", "dibs", strings.TrimPrefix(r.report.Tag, "v"))
	if p.oldBin == p.newBin {
		return p, errors.New("candidate legacy proof must advance beyond v0.0.16")
	}
	for _, dir := range []string{p.home, p.oldBin, p.newBin, filepath.Join(p.prefix, "bin")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return p, err
		}
	}
	if err := stageProofBinaries(old, p.oldBin); err != nil {
		return p, err
	}
	if err := stageProofBinaries(candidate, p.newBin); err != nil {
		return p, err
	}
	for _, name := range []string{"dibs", "dibd"} {
		if err := os.Symlink(filepath.Join(p.newBin, name), filepath.Join(p.prefix, "bin", name)); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (r *runner) configureCommand(name string, env []string, cli string) (string, error) {
	return r.command(name, env, cli, "configure", "--service")
}

func (r *runner) candidateServiceProof(candidate string) error {
	p, err := r.prepareServiceProof(candidate)
	if err != nil {
		return err
	}
	frozenPath := filepath.Join(p.prefix, "Homebrew", "Library", "Homebrew", "shims", "shared") +
		":" + paths.DefaultServicePath
	env := append(isolatedEnv(p.home, r.root), "PATH="+frozenPath)
	oldCLI, newCLI := filepath.Join(p.oldBin, "dibs"), filepath.Join(p.newBin, "dibs")
	if _, err := r.configureCommand("published-control-configure-first", env, oldCLI); err != nil {
		return err
	}
	unit := filepath.Join(p.home, ".config", "systemd", "user", "dibs.service")
	body, err := os.ReadFile(unit) // #nosec G304 -- fixed unit in the new private legacy-fixture home
	frozen := frozenPublishedUnit(filepath.Join(p.oldBin, "dibd"), filepath.Join(p.home, ".dibs"), frozenPath)
	if err != nil || string(body) != frozen {
		return errors.New("old binary did not emit the exact frozen published headerless unit; setup is not qualified")
	}
	out, err := r.configureCommand("published-control-repeat-intended-red", env, oldCLI)
	if err == nil || !strings.Contains(out, "already exists") {
		return errors.New("old-code repeated-configure control did not fail for the intended existing-unit refusal")
	}
	r.report.Findings = append(r.report.Findings, finding{
		Step:    "published-control-repeat-intended-red",
		Warning: "v0.0.16 real configure repeats refuse their own headerless unit (expected negative control)",
		FixedBy: "PR #517 managed-unit receipts and frozen migration; PR #519 removed Linux cask postflight",
	})
	if _, err := r.configureCommand("candidate-headerless-migration", env, newCLI); err != nil {
		return err
	}
	migrated, err := os.ReadFile(unit) // #nosec G304 -- same fixed legacy-fixture unit after the real configure command
	stable := filepath.Join(p.prefix, "bin", "dibd")
	if err != nil || bytes.Equal(body, migrated) || !bytes.Contains(migrated, []byte(stable)) ||
		!bytes.HasPrefix(migrated, []byte("# written by dibs configure --service; sha256 of the rest: ")) {
		return errors.New("actual candidate command did not migrate the frozen unit to its stable package-manager link")
	}
	if _, err := r.configureCommand("candidate-migrated-repeat", env, newCLI); err != nil {
		return err
	}
	after, err := os.ReadFile(unit) // #nosec G304 -- same fixed fixture unit, repeat comparison
	if err != nil || !bytes.Equal(after, migrated) {
		return errors.New("repeated migrated configure changed the unit")
	}
	return nil
}

func stageProofBinaries(source, target string) error {
	for _, name := range []string{"dibs", "dibd"} {
		path := filepath.Join(target, name)
		if err := copyCandidateFile(filepath.Join(source, name), path); err != nil {
			return err
		}
		if err := os.Chmod(path, 0o755); err != nil { // #nosec G302 -- verified binaries must be executable
			return err
		}
	}
	return nil
}

// Frozen v0.0.16 output, independent of the current service generator. The
// control first requires that the actual published binary emitted these bytes.
func frozenPublishedUnit(daemon, dir, searchPath string) string {
	return fmt.Sprintf("[Unit]\nDescription=Dibs: coordination board for AI agents\n"+
		"Documentation=https://github.com/agenxy/dibs\nAfter=network.target\n\n"+
		"[Service]\nType=simple\nEnvironment=\"PATH=%s\"\nExecStart=\"%s\" -dir \"%s\"\n"+
		"Restart=on-failure\nRestartSec=2\n\n[Install]\nWantedBy=default.target\n", searchPath, daemon, dir)
}
