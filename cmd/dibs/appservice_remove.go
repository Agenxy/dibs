// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/agenxy/dibs/internal/paths"
)

func removeAppService() error {
	if runtime.GOOS != "darwin" {
		return errors.New("configure --service --remove is the macOS app-service removal " +
			"command; use systemctl --user disable --now dibs on Linux")
	}
	dir, err := appRemovalDirectory()
	if err != nil {
		return err
	}
	app, err := appRemovalLocation()
	if err != nil {
		return err
	}
	if err := validateAppServicePayload(app); err != nil {
		return err
	}
	legacy := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "org.agenxy.dibs.plist")
	old, err := readLegacyAppService(legacy)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if old != nil && filepath.Clean(old.dir) != filepath.Clean(dir) {
		return fmt.Errorf("legacy service belongs to %s, not %s; it was left untouched", old.dir, dir)
	}
	if err := stopDaemon(dir); err != nil {
		return err
	}
	if err := unregisterAppService(app); err != nil {
		return err
	}
	if old != nil {
		if err := unloadLegacyAppService(); err != nil {
			return err
		}
		// #nosec G703 -- fixed, validated legacy unit, removed only by explicit --remove.
		if err := os.Remove(legacy); err != nil {
			return err
		}
	}
	fmt.Println("Dibs background service is unregistered. The board and its directory pin were kept.")
	return nil
}

func appRemovalDirectory() (string, error) {
	selected, err := paths.SelectDir(false)
	if err != nil {
		return "", err
	}
	pin, found, err := paths.PinnedService()
	if err != nil {
		return "", err
	}
	if found && filepath.Clean(pin.Dir) != filepath.Clean(selected.Dir) {
		return "", fmt.Errorf("the app service belongs to %s, not %s; it was left untouched", pin.Dir, selected.Dir)
	}
	return selected.Dir, nil
}

func appRemovalLocation() (string, error) {
	installed, err := daemonPath()
	if err != nil {
		return "", err
	}
	app, err := pairedServiceApp(installed)
	if err != nil {
		return "", err
	}
	if app == "" {
		return "", errors.New("the installed Dibs.app is missing; restore it before unregistering its service")
	}
	return app, nil
}
