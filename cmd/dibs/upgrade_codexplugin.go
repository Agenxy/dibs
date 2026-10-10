// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// A plugin failure is not a reason to withhold a daemon fix. This entry runs
// even when the daemon already serves the installed build; catalog repair is a
// separate job and its failure remains visible to doctor after the upgrade.
func refreshUpgradeCodexPlugin(out io.Writer, dry bool) {
	home, err := codexPluginHome()
	if err != nil {
		warnCodexPluginRefresh(out, "", err)
		return
	}
	installed, err := installedCodexPlugin(home)
	if err != nil {
		warnCodexPluginRefresh(out, home, err)
		return
	}
	if !installed {
		return
	}
	p, err := planCodexPlugin("")
	if err == nil {
		if dry {
			err = p.report(out)
		} else {
			err = p.install(out)
		}
	}
	if err != nil {
		if dry {
			_, _ = fmt.Fprintf(out, "WARNING: plugin refresh unavailable: %v; after fixing it, run %s. "+
				"Daemon upgrade is independent.\n", err, codexPluginRepair)
			return
		}
		warnCodexPluginRefresh(out, home, err)
	}
}

func warnCodexPluginRefresh(out io.Writer, home string, err error) {
	_, _ = fmt.Fprintf(out, "WARNING: Codex plugin refresh failed: %v\nRun %s. "+
		"Continuing the daemon upgrade; running chats keep their old tool list.\n", err, codexPluginRepair)
	if home != "" {
		path := filepath.Join(home, "dibs-marketplace", "refresh-failed")
		if markErr := writePluginFile(path, []byte(err.Error())); markErr != nil {
			_, _ = fmt.Fprintf(out, "WARNING: could not retain plugin-refresh diagnosis: %v\n", markErr)
		}
	}
}

func (d *diagnosis) checkCodexPluginVersion() {
	home, err := codexPluginHome()
	if err != nil {
		d.bad("cannot locate Codex plugin state: "+err.Error(), codexPluginRepair)
		return
	}
	marker := filepath.Join(home, "dibs-marketplace", "refresh-failed")
	if _, err := os.Stat(marker); err == nil {
		d.bad("the last Codex plugin refresh failed; daemon upgrades continue independently", codexPluginRepair)
		return
	} else if !os.IsNotExist(err) {
		d.bad("cannot read Codex plugin refresh diagnosis: "+err.Error(), codexPluginRepair)
		return
	}
	installed, err := installedCodexPlugin(home)
	if err != nil {
		d.bad("cannot inspect Codex plugin installation: "+err.Error(), codexPluginRepair)
		return
	}
	if !installed {
		return
	}
	// Inspection needs no executable. Construct the same expected payload, but
	// never invoke an installer or contact the app from doctor.
	p, err := planCodexPlugin("not-run-by-doctor")
	if err != nil || !p.current() {
		d.bad("Codex's Dibs plugin does not have a verified current version root; "+
			"running chats may still use an older tool list", codexPluginRepair)
		return
	}
	d.ok("Codex's Dibs plugin has the current verified version root; " +
		"existing chats still require a genuine app refresh or to end")
}
