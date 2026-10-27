// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const backgroundSettings = "System Settings → General → Login Items & Extensions → Allow in the Background → Dibs"

const orphanServiceRepair = "Restore Dibs using the same install channel, then run " +
	"dibs configure --service --remove. " +
	"For Homebrew: brew install --cask agenxy/tap/dibs; dibs configure --service --remove; brew uninstall --cask dibs. " +
	"Restoring Dibs briefly starts the service before removal. " +
	"Switch off the leftover item meanwhile in " + backgroundSettings

// Only inspect this user's loaded job. Background-item history has no
// prompt-free query here; absence from launchctl is not proof of no record.
var errAppServiceNotLoaded = errors.New("dibs app service is not loaded")

var readAppServiceJob = readLoadedAppService

var appServiceDiagnostic = executeAppServiceDiagnostic

var loadedProcessExecutable = processExecutable

func readLoadedAppService() ([]byte, error) {
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), appServiceLabel)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// #nosec G204 -- fixed print command and target derived only from this process's UID and canonical label.
	cmd := exec.CommandContext(ctx, "/bin/launchctl", "print", target)
	cmd.WaitDelay = time.Second
	body, err := appServiceDiagnostic(cmd)
	if err != nil && (strings.Contains(string(body), "Could not find service") ||
		strings.Contains(string(body), "Could not find specified service")) {
		return nil, errAppServiceNotLoaded
	}
	return body, err
}

func executeAppServiceDiagnostic(cmd *exec.Cmd) ([]byte, error) {
	var out appServiceDiagnosticOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}

type appServiceDiagnosticOutput struct{ bytes.Buffer }

func (b *appServiceDiagnosticOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("app-service diagnostic exceeds 1 MiB")
	}
	return b.Buffer.Write(p)
}

// errAppServiceNotRunning is a loaded SMAppService job with no process: its
// program is resolved by macOS from the app that registered it, and no
// prompt-free reading here can name that app.
var errAppServiceNotRunning = errors.New("dibs app service is loaded but not running")

// loadedAppProgram names the executable the loaded job runs.
//
// Two shapes, because launchd prints two. A plain LaunchAgent has an
// absolute `program = …` line. The job SMAppService loads, which is the one
// every current install has, prints only `program identifier =
// Contents/MacOS/dibd`, relative to the app that registered it, and no
// absolute path anywhere. This read only the first shape, its test fixture
// invented that line, and doctor told every real install it "cannot
// identify the loaded Dibs program" beside a passing check that the service
// starts the installed dibd. A running job's pid names its executable
// through the kernel, which is the path it was really started from.
func loadedAppProgram(body []byte) (string, error) {
	target := fmt.Sprintf("gui/%d/%s = {", os.Getuid(), appServiceLabel)
	if !strings.HasPrefix(strings.TrimSpace(string(body)), target) {
		return "", errors.New("loaded service diagnostic did not identify this user's Dibs job")
	}
	relative, pid := "", 0
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if value, found := strings.CutPrefix(trimmed, "program = "); found {
			return checkedAppProgram(strings.TrimSpace(value))
		}
		if value, found := strings.CutPrefix(trimmed, "program identifier = "); found {
			relative, _, _ = strings.Cut(value, " (")
		}
		// The job's own pid is one tab in; nested dictionaries repeat the key deeper.
		if value, found := strings.CutPrefix(line, "\tpid = "); found && pid == 0 {
			pid, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	if relative == "" {
		return "", errors.New("loaded Dibs service did not expose its program")
	}
	if relative != filepath.Join("Contents", "MacOS", "dibd") {
		return "", errors.New("loaded Dibs service has an unexpected program; inspect it in " + backgroundSettings)
	}
	if pid <= 0 {
		return "", errAppServiceNotRunning
	}
	program, err := loadedProcessExecutable(pid)
	if err != nil {
		return "", fmt.Errorf("could not read the running service's program: %w", err)
	}
	return checkedAppProgram(program)
}

func checkedAppProgram(program string) (string, error) {
	app := filepath.Dir(filepath.Dir(filepath.Dir(program)))
	if !filepath.IsAbs(program) || filepath.Base(app) != "Dibs.app" ||
		program != filepath.Join(app, "Contents", "MacOS", "dibd") {
		return "", errors.New("loaded Dibs service has an unexpected program; inspect it in " + backgroundSettings)
	}
	return program, nil
}

func checkOrphanedAppService(note func(string), warn fixFn) {
	if runtime.GOOS != "darwin" {
		return
	}
	body, err := readAppServiceJob()
	if errors.Is(err, errAppServiceNotLoaded) {
		note("Dibs app service is not loaded. Unloaded background-item history cannot be inspected " +
			"without authorization; check " + backgroundSettings + " for a leftover row.")
		return
	}
	if err != nil {
		warn("cannot inspect the loaded Dibs app service", err.Error()+"; check "+backgroundSettings)
		return
	}
	program, err := loadedAppProgram(body)
	if errors.Is(err, errAppServiceNotRunning) {
		note("Dibs app service is loaded but not running. macOS resolves its program from the app " +
			"that registered it, which cannot be read here without authorization; check " + backgroundSettings + ".")
		return
	}
	if err != nil {
		warn("cannot identify the loaded Dibs program", err.Error()+"; check "+backgroundSettings)
		return
	}
	if _, err := os.Stat(program); err == nil { // #nosec G703 -- fixed app program from own loaded launchctl job.
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		warn("cannot inspect the loaded Dibs program", err.Error()+"; check "+backgroundSettings)
		return
	}
	// A missing executable may still be mapped by this loaded job. Never call
	// it inert, and do not claim visibility into unloaded Settings records.
	warn("Dibs has an orphaned loaded background service; its missing program cannot start",
		program+". An existing process may still be running. "+orphanServiceRepair)
}
