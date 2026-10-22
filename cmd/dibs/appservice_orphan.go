// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

// A diagnostic only: never reset the machine-wide background-item database.
var readAppBackgroundItems = func() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sfltool", "dumpbtm")
	cmd.WaitDelay = time.Second
	var out backgroundItemOutput
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type backgroundItemOutput struct{ bytes.Buffer }

func (b *backgroundItemOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("background-item diagnostic exceeds 1 MiB")
	}
	return b.Buffer.Write(p)
}

// The measured macOS 27 dump is per UID and contains typed identifiers. Names
// alone cannot distinguish Dibs from an unrelated app or our historical probes.
func appBackgroundRecords(body []byte, uid int) []map[string]string {
	var result []map[string]string
	var record map[string]string
	mine := false
	flush := func() {
		if mine && record != nil {
			result = append(result, record)
		}
		record = nil
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Records for UID ") {
			flush()
			mine = strings.HasPrefix(line, "Records for UID "+strconv.Itoa(uid)+" :")
			continue
		}
		if strings.HasPrefix(line, "UUID:") {
			flush()
			record = map[string]string{}
		}
		if record != nil {
			if key, value, found := strings.Cut(line, ":"); found {
				record[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
		}
	}
	flush()
	return result
}

func orphanedAppProgram(body []byte, uid int) (string, error) {
	records := appBackgroundRecords(body, uid)
	agent := false
	for _, r := range records {
		if r["Identifier"] == "8."+appServiceLabel &&
			r["Parent Identifier"] == "2.org.agenxy.dibs" && strings.Contains(r["Disposition"], "[enabled,") {
			agent = true
		}
	}
	if !agent {
		return "", nil
	}
	var missing string
	for _, r := range records {
		if r["Identifier"] != "2.org.agenxy.dibs" || r["Bundle Identifier"] != "org.agenxy.dibs" {
			continue
		}
		app := r["URL"]
		if !filepath.IsAbs(app) || filepath.Base(app) != "Dibs.app" {
			continue
		}
		program := filepath.Join(app, "Contents", "MacOS", "dibd")
		_, err := os.Stat(program) // #nosec G703 -- read-only fixed program below the OS-recorded Dibs app
		if err == nil {
			return "", nil
		} // another current canonical record is usable
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		missing = program
	}
	return missing, nil
}

func checkOrphanedAppService(warn fixFn) {
	if runtime.GOOS != "darwin" {
		return
	}
	body, err := readAppBackgroundItems()
	if err != nil {
		warn("cannot inspect Dibs background-item records", err.Error())
		return
	}
	program, err := orphanedAppProgram(body, os.Getuid())
	if err != nil {
		warn("cannot inspect the registered Dibs program", err.Error())
		return
	}
	if program == "" {
		return
	}
	// Deleted executable bytes can remain mapped by an already-running daemon.
	// Do not call a missing path proof that such a process has already exited.
	what := "Dibs has an orphaned background-item registration; its missing program cannot start"
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), appServiceLabel)
	out, probeErr := runAppLaunchctl("print", target)
	if probeErr != nil && (strings.Contains(string(out), "Could not find service") ||
		strings.Contains(string(out), "Could not find specified service")) {
		what = "Dibs has an inert orphaned background-item registration; its program is missing and no job is loaded"
	}
	warn(what, program+". "+orphanServiceRepair)
}
