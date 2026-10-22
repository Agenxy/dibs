// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/agenxy/dibs/internal/paths"
)

// Keep the legacy unit and old pin until the replacement serves the board.
// A registration receipt is only enablement; p.doConfirm proves the fleet.
func (p *plan) cutoverAppService() (failure error) {
	a := p.app
	if err := a.preflight(); err != nil {
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
	hadPin := err == nil
	stopped, changedPin, touchedApp, legacyOut := false, false, false, false
	defer func() {
		if failure == nil {
			return
		}
		// Remove the new supervisor BEFORE bringing back the old one. If its
		// absence cannot be confirmed, leave one explicit failure, never race
		// a second supervisor against an uncertain first one.
		if touchedApp {
			status, err := appServiceCall(a.app, "unregister")
			if err != nil || status.Status != "not-registered" {
				failure = errors.Join(failure, fmt.Errorf("cannot confirm app service removal (%s, %v); the legacy job was not started; run dibs doctor", status.Status, err))
				return
			}
		}
		if changedPin {
			if hadPin {
				err = restoreServicePin(pin, before)
			} else {
				err = os.Remove(pin)
			}
			if err != nil {
				failure = errors.Join(failure, fmt.Errorf("restoring service directory pin: %w", err))
				return
			}
		}
		if stopped {
			if a.legacy != "" {
				if legacyOut {
					err = reloadUnit(a.legacy)
				} else {
					err = kickstartUnit(a.legacy)
				}
				if err == nil {
					err = p.doConfirm(p.dir)
				}
			} else {
				// The same app owned the service before this upgrade. Restore its
				// pin and registration; never fall back to a detached process.
				err = a.start()
				if err == nil {
					err = p.doConfirm(p.dir)
				}
			}
			if err != nil {
				failure = errors.Join(failure, fmt.Errorf("service recovery did not complete: %w; run dibs doctor", err))
			}
		}
	}()
	if p.serving || p.running.addr != "" || p.running.unknown {
		stopped = true
		if err := p.doStop(p.dir); err != nil {
			return err
		}
	}
	if a.legacy != "" {
		target := fmt.Sprintf("gui/%d/org.agenxy.dibs", os.Getuid())
		// Missing jobs are harmless, but any other refusal is not permission
		// to register a second supervisor. Check the resulting state.
		_ = exec.Command("/bin/launchctl", "bootout", target).Run()
		if out, err := exec.Command("/bin/launchctl", "print", target).CombinedOutput(); err == nil {
			return fmt.Errorf("legacy service is still loaded; refusing a second supervisor: %s", out)
		}
		legacyOut = true
	} else {
		touchedApp = true
		status, err := appServiceCall(a.app, "unregister")
		if err != nil {
			return err
		}
		if status.Status != "not-registered" {
			return fmt.Errorf("old app registration is still %s; refusing duplicate registration", status.Status)
		}
	}
	if err := a.writePin(p.dir); err != nil {
		return err
	}
	changedPin = true
	touchedApp = true
	if err := a.start(); err != nil {
		return err
	}
	if err := p.doConfirm(p.dir); err != nil {
		return err
	}
	if a.legacy != "" {
		// The new service is already proven. Preserve the operator's old bytes
		// as a private receipt, then remove the obsolete login declaration.
		if err := os.WriteFile(a.legacy+".migrated", a.legacyBody, 0o600); err != nil {
			return err
		}
		if err := os.Remove(a.legacy); err != nil {
			return err
		}
	}
	requestInstalledFolderAccess()
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
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
