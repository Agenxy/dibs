// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// macOS 27.0 (26A428), 2026-10-27: loaded residence of 3, 6 and 9 s
// launched 0/3 replacements each; 12 s launched 3/3, 15 s 10/10 and
// 20 s 10/10. This is an empirical bound with no visible refresh signal,
// not an OS guarantee. Only the short-lived installer waits, with the board
// already verified serving; neither the daemon nor its bridge has a timer.
const appServiceRefreshResidence = 20 * time.Second

// Tests substitute only this external time boundary, through real configure.
var waitForAppServiceRefresh = time.Sleep

func (tx *appCutover) refreshNativeService(first error) error {
	// A positive, fresh CODESIGNING refusal is the only reason to retain a
	// failed native job beside a manual daemon. The daemon's ledger lock still
	// excludes a second writer if launchd attempts another launch meanwhile.
	if err := tx.restorePreviousSupervisor(true); err != nil {
		return errors.Join(first, fmt.Errorf("starting the board during native refresh: %w", err))
	}
	tx.nativeRefresh, tx.manualServing, tx.retainFailed = true, true, true
	fmt.Fprintf(os.Stderr, "macOS refused the new build's first launch; the board is serving meanwhile; "+
		"waiting %.0f s before registering the service again\n", appServiceRefreshResidence.Seconds())
	waitForAppServiceRefresh(appServiceRefreshResidence)
	// Re-read the registry after the hold: the old plan's PID belonged to the
	// daemon stopped before registration, not the board now serving this dir.
	if observeAppServiceLaunch().owns(runningDaemon(tx.p.dir).pid) {
		tx.p.launchFailure = nil
		return tx.p.doConfirm(tx.p.dir)
	}
	if err := tx.p.doStop(tx.p.dir); err != nil {
		return errors.Join(first, err)
	}
	tx.manualServing = false
	if err := unregisterAppService(tx.p.app.app); err != nil {
		return errors.Join(first, err)
	}
	tx.retainFailed = false
	if err := tx.p.startAppService(); err != nil {
		return errors.Join(first, err)
	}
	if err := tx.p.doConfirm(tx.p.dir); err != nil {
		tx.retainFailed = errors.Is(err, errAppServiceLaunchRefused)
		return errors.Join(first, err)
	}
	return nil
}

// Replace only verify's generic foreground recipe after verified recovery.
// Keep the complete error tree, including siblings and wrapper context: a
// second registration failure must not erase the first launch refusal.
func noteStartupRecovered(err error) {
	var startup *boardStartFailure
	if errors.As(err, &startup) {
		startup.recovered = true
	}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		for _, child := range joined.Unwrap() {
			noteStartupRecovered(child)
		}
	}
}

func (tx *appCutover) recoverAfterNativeRefresh() error {
	// Preserve the second positively failed registration so its residence can
	// accumulate. An uncertain registration still requires confirmed removal
	// before another process starts, just like every other cutover failure.
	if !tx.retainFailed {
		if err := unregisterAppService(tx.p.app.app); err != nil {
			return err
		}
	}
	var err error
	if tx.manualServing {
		tx.p.launchFailure = nil
		err = tx.p.doConfirm(tx.p.dir)
	} else {
		err = tx.restorePreviousSupervisor(true)
	}
	if err != nil {
		return fmt.Errorf("service recovery did not complete: %w; run dibs doctor", err)
	}
	tx.recovery = "the board is serving unsupervised; the bounded native-service retry did not complete. " +
		"Run `dibs doctor`, then retry `dibs configure --service`. " +
		"The upgrade still failed. This is the NEW build; the previous binary was not restored."
	fmt.Fprintln(os.Stderr, "recovered: "+tx.recovery)
	return nil
}
