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

func loadedAppProgram(body []byte) (string, error) {
	target := fmt.Sprintf("gui/%d/%s = {", os.Getuid(), appServiceLabel)
	if !strings.HasPrefix(strings.TrimSpace(string(body)), target) {
		return "", errors.New("loaded service diagnostic did not identify this user's Dibs job")
	}
	for _, line := range strings.Split(string(body), "\n") {
		value, found := strings.CutPrefix(strings.TrimSpace(line), "program = ")
		if !found {
			continue
		}
		program := strings.TrimSpace(value)
		app := filepath.Dir(filepath.Dir(filepath.Dir(program)))
		if !filepath.IsAbs(program) || filepath.Base(app) != "Dibs.app" ||
			program != filepath.Join(app, "Contents", "MacOS", "dibd") {
			return "", errors.New("loaded Dibs service has an unexpected program; inspect it in " + backgroundSettings)
		}
		return program, nil
	}
	return "", errors.New("loaded Dibs service did not expose its program")
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
