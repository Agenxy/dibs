// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Native registration says enabled even when launchd immediately refuses the
// executable. Capture the pre-registration job so its old last-exit reason
// cannot be mistaken for the outcome of the attempt we are waiting for.
func (p *plan) startAppService() error {
	before := observeAppServiceLaunch()
	p.launchFailure = func() error {
		after := observeAppServiceLaunch()
		if !before.known || !after.known || after.absent || after.pid != 0 ||
			after.state != "spawn failed" || after.reason != "OS_REASON_CODESIGNING" || after.runs == 0 {
			return nil
		}
		fresh := before.absent || (before.uuid != "" && after.uuid != "" &&
			(before.uuid != after.uuid || after.runs > before.runs))
		if !fresh {
			return nil
		}
		return errors.New("macOS refused the Dibs service launch (OS_REASON_CODESIGNING); " +
			"enabled registration did not launch the daemon. Run dibs doctor to inspect the installed app, " +
			"then retry dibs configure --service. The launch constraint was not changed")
	}
	return p.app.start()
}

type appServiceLaunch struct {
	known, absent bool
	uuid          string
	runs, pid     uint64
	state, reason string
}

func observeAppServiceLaunch() appServiceLaunch {
	body, err := readAppServiceJob()
	if errors.Is(err, errAppServiceNotLoaded) {
		return appServiceLaunch{known: true, absent: true}
	}
	if err != nil {
		return appServiceLaunch{}
	}
	return parseAppServiceLaunch(body)
}

// Read only the exact user's job and its one-tab fields. Nested dictionaries
// repeat names such as pid and do not describe the job's current outcome.
// Unknown output remains unknown; it neither proves refusal nor replaces the
// existing board/replay verification and its bound.
func parseAppServiceLaunch(body []byte) appServiceLaunch {
	target := fmt.Sprintf("gui/%d/%s = {", os.Getuid(), appServiceLabel)
	if !strings.HasPrefix(strings.TrimSpace(string(body)), target+"\n") {
		return appServiceLaunch{}
	}
	result := appServiceLaunch{}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		line, ok := strings.CutPrefix(line, "\t")
		if !ok || strings.HasPrefix(line, "\t") {
			continue
		}
		key, value, ok := strings.Cut(line, " = ")
		if !ok || (key != "BTM uuid" && key != "runs" && key != "pid" && key != "job state" && key != "last exit reason") {
			continue
		}
		if seen[key] {
			return appServiceLaunch{}
		}
		seen[key] = true
		if !result.readField(key, strings.TrimSpace(value)) {
			return appServiceLaunch{}
		}
	}
	result.known = seen["runs"] && seen["job state"]
	return result
}

func (r *appServiceLaunch) readField(key, value string) bool {
	switch key {
	case "BTM uuid":
		r.uuid = value
	case "job state":
		r.state = value
	case "last exit reason":
		r.reason = value
	default: // Only the two allowlisted numeric fields reach here.
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return false
		}
		if key == "pid" {
			r.pid = n
		} else {
			r.runs = n
		}
	}
	return true
}
