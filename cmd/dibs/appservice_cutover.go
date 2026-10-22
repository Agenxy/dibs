// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/paths"
)

// Keep the legacy unit and old pin until the replacement serves the board.
// A registration receipt is only enablement; p.doConfirm proves the fleet.
type appCutover struct {
	p                                          *plan
	pin                                        string
	before                                     []byte
	hadPin                                     bool
	stopped, changedPin, touchedApp, legacyOut bool
}

func (p *plan) cutoverAppService() (failure error) {
	if err := p.app.preflight(); err != nil {
		return err
	}
	if err := p.appStartupMatches(); err != nil {
		return err
	}
	if err := checkServicePinWritable(); err != nil {
		return err
	}
	pin, err := paths.ServiceConfigPath()
	if err != nil {
		return err
	}
	before, err := os.ReadFile(pin) // #nosec G304 -- fixed private service configuration
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tx := &appCutover{p: p, pin: pin, before: before, hadPin: err == nil}
	defer func() {
		if failure != nil {
			failure = errors.Join(failure, tx.rollback())
		}
	}()
	return tx.run()
}

func (tx *appCutover) run() error {
	p := tx.p
	if p.serving || p.running.addr != "" || p.running.unknown {
		tx.stopped = true
		if err := p.doStop(p.dir); err != nil {
			return err
		}
	}
	if err := tx.removePreviousSupervisor(); err != nil {
		return err
	}
	if err := p.app.writePin(p.dir); err != nil {
		return err
	}
	tx.changedPin = true
	tx.touchedApp = true
	if err := p.app.start(); err != nil {
		return err
	}
	if err := p.doConfirm(p.dir); err != nil {
		return err
	}
	if err := retainLegacyAppReceipt(p.app); err != nil {
		return err
	}
	requestInstalledFolderAccess(p.dir)
	return nil
}

func (tx *appCutover) removePreviousSupervisor() error {
	if tx.p.app.legacy != "" {
		if err := unloadLegacyAppService(); err != nil {
			return err
		}
		tx.legacyOut = true
		return nil
	}
	tx.touchedApp = true
	return unregisterAppService(tx.p.app.app)
}

func unregisterAppService(app string) error {
	status, err := appServiceCall(app, "unregister")
	if err != nil {
		return fmt.Errorf("cannot confirm app service removal; a second supervisor was not started: %w", err)
	}
	if status.Status != "not-registered" {
		return fmt.Errorf("cannot confirm app service removal (%s); a second supervisor was not started", status.Status)
	}
	return nil
}

func (tx *appCutover) rollback() error {
	// Remove the replacement BEFORE restoring the old supervisor. Uncertainty
	// refuses a second supervisor, even if that leaves a visible recovery error.
	if tx.touchedApp {
		if err := unregisterAppService(tx.p.app.app); err != nil {
			return err
		}
	}
	if tx.changedPin && !tx.p.app.repairPin {
		var err error
		if tx.hadPin {
			err = restoreServicePin(tx.pin, tx.before)
		} else {
			err = os.Remove(tx.pin)
		}
		if err != nil {
			return fmt.Errorf("restoring service directory pin: %w", err)
		}
	}
	if !tx.stopped && !tx.legacyOut {
		return nil
	}
	if err := tx.restorePreviousSupervisor(); err != nil {
		return fmt.Errorf("service recovery did not complete: %w; run dibs doctor", err)
	}
	return nil
}

func (tx *appCutover) restorePreviousSupervisor() error {
	var err error
	switch {
	case tx.p.app.legacy != "":
		err = restoreLegacyAppService(tx.p.app.legacy, tx.legacyOut)
	case tx.p.app.previousManual:
		if tx.p.start != nil {
			err = tx.p.start(tx.p.installed, tx.p.dir, "", tx.p.running)
		} else {
			err = startManualAppRecovery(tx.p.installed, tx.p.dir, "", tx.p.running)
		}
	default:
		err = tx.p.app.start()
	}
	if err != nil {
		return err
	}
	return tx.p.doConfirm(tx.p.dir)
}

// The existing process-start boundary, isolated so transaction tests never
// launch a real recovery daemon while driving a refusal through configure.
var startManualAppRecovery = startDaemon

func retainLegacyAppReceipt(a *appServicePlan) error {
	if a.legacy == "" {
		return nil
	}
	// No clobber: the private receipt is evidence, not a mutable backup slot.
	f, err := os.OpenFile(a.legacy+".migrated", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	switch {
	case errors.Is(err, os.ErrExist):
		before, readErr := os.ReadFile(a.legacy + ".migrated")
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(before, a.legacyBody) {
			return errors.New("legacy migration receipt changed; inspect it before retrying")
		}
	case err != nil:
		return err
	default:
		defer func() { _ = f.Close() }()
		if _, err := f.Write(a.legacyBody); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
	}
	return os.Remove(a.legacy)
}

// The executable seam lets transaction tests record supervisor transitions
// without touching the developer's real org.agenxy.dibs job. The production
// implementation always uses the system launchctl, never PATH or a shell.
var runAppLaunchctl = func(args ...string) ([]byte, error) {
	// #nosec G204 -- fixed system executable, owned service arguments
	return exec.Command("/bin/launchctl", args...).CombinedOutput()
}

func unloadLegacyAppService() error {
	target := fmt.Sprintf("gui/%d/org.agenxy.dibs", os.Getuid())
	_, _ = runAppLaunchctl("bootout", target)
	out, err := runAppLaunchctl("print", target)
	if err == nil {
		return errors.New("legacy service is still loaded; refusing a second supervisor")
	}
	if strings.Contains(string(out), "Could not find service") ||
		strings.Contains(string(out), "Could not find specified service") {
		return nil
	}
	return fmt.Errorf("cannot confirm legacy service removal; refusing a second supervisor: %w: %s", err, out)
}

func restoreLegacyAppService(unit string, unloaded bool) error {
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	if unloaded {
		if out, err := runAppLaunchctl("bootstrap", domain, unit); err != nil {
			return fmt.Errorf("restore legacy service: %w: %s", err, out)
		}
	}
	if out, err := runAppLaunchctl("kickstart", domain+"/org.agenxy.dibs"); err != nil {
		return fmt.Errorf("restart legacy service: %w: %s", err, out)
	}
	return nil
}

func restoreServicePin(path string, body []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".service-restore-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// The static app manifest starts from configuration, so a live flag override
// must be represented there before cutover. Never approve one address then
// silently start another, or discard a deliberate parallel-board setting.
func (p *plan) appStartupMatches() error {
	if p.app == nil || p.running.addr == "" {
		return nil
	}
	if p.running.parallel {
		return errors.New("the running daemon permits parallel boards; move that policy to a supported " +
			"service configuration before app migration; nothing was stopped")
	}
	want, err := readConfiguredAddr(p.dir)
	if err != nil {
		return err
	}
	if want == "" {
		want = defaultAddr
	}
	if !sameHostPort(hostPort(want), hostPort(p.running.addr)) {
		return fmt.Errorf("the running daemon listens on %s, but the app service configuration names %s; "+
			"put the intended address in dibs.toml before migration; nothing was stopped", p.running.addr, want)
	}
	return nil
}

func (p *plan) preflightAppService() error {
	if p.moveDir {
		return errors.New("the app service preserves its existing directory; migrate the " +
			"service with dibs upgrade before requesting a directory rename")
	}
	if err := p.app.preflight(); err != nil {
		return err
	}
	if err := p.appStartupMatches(); err != nil {
		return err
	}
	return checkServicePinWritable()
}
