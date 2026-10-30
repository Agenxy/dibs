// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/agenxy/dibs/internal/boardconfig"
	"github.com/agenxy/dibs/internal/localtransport"
)

func (d *diagnosis) checkIPCDirectory(dir string) bool {
	if !localtransport.Supported() {
		if d.ipcPreflight {
			d.bad("local IPC preflight is unavailable on this platform", localtransport.UnsupportedHint)
			return false
		}
		return true
	}
	status, err := localtransport.InspectDirectory(dir)
	if errors.Is(err, os.ErrNotExist) {
		if d.ipcPreflight {
			d.bad("local data directory does not exist yet", "select the existing board before assessing an upgrade")
			return false
		}
		d.warn("local data directory does not exist yet", "start dibd to create its private directory before IPC cutover")
		return !d.ipcPreflight
	}
	if err != nil {
		what := "local IPC directory preflight refuses this profile"
		if status.Mode != "" {
			what += fmt.Sprintf(" (mode %s, uid %d, gid %d)", status.Mode, status.UID, status.GID)
		}
		d.bad(what, err.Error())
		return false
	}
	if !d.checkConfiguredTLSKey(dir) {
		return false
	}
	metadata := fmt.Sprintf("mode %s, uid %d, gid %d", status.Mode, status.UID, status.GID)
	if status.WillTighten {
		d.warn("local IPC directory will be tightened from "+status.Mode+" to 0700 ("+metadata+")",
			"the upgraded daemon can tighten this owned directory through its handle; other OS users cannot write it. "+
				"preflight has changed nothing. Complete the expected-client transport inventory before restarting")
		return !d.ipcPreflight
	}
	d.ok("local IPC directory is private (" + metadata + ")")
	notice, err := localtransport.ConsumeTightening(dir)
	if err != nil {
		d.warn("directory-tightening history is unknown", err.Error())
	} else if notice != nil {
		d.warn("local IPC directory tightened from "+notice.FromMode+" at "+notice.At.Format(time.RFC3339),
			"startup completed the safe permission change; this clean check acknowledges the derived notice")
	}
	return true
}

func (d *diagnosis) checkInheritedDirectory(inherited string) {
	// Doctor exists to explain a directory the current version would not have
	// created. Give one ordered command: moving the directory before stopping
	// its service, or repointing the service before moving it, leaves it broken.
	if inherited == "" {
		return
	}
	fix := "nothing is wrong and nothing is required. To adopt the current name, " +
		"`dibs upgrade --adopt-dir`, which stops the daemon, moves the directory, " +
		"repoints the service and starts it again in that order"
	if unit := unitPinning(inherited); unit != "" {
		fix += " (" + unit + " pins the old path, so a bare `mv` would leave the " +
			"service starting against a directory that is gone)"
	}
	d.warn("data directory is "+inherited+", named by an older version", fix)
}

func (d *diagnosis) checkConfiguredTLSKey(dir string) bool {
	cfg, err := boardconfig.Load(dir)
	if err != nil {
		d.bad("cannot preflight the selected TLS key configuration", err.Error())
		return false
	}
	if cfg.TLSKey != "" {
		if err := localtransport.PrivateFile(cfg.TLSKey, false); err != nil {
			d.bad("configured TLS private key is unsafe or unavailable", err.Error())
			return false
		}
	}
	return true
}
