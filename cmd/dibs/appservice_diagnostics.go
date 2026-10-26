// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/agenxy/dibs/internal/paths"
)

func appForServiceUnit(unit string) string {
	if runtime.GOOS == "darwin" && strings.HasSuffix(unit, string(filepath.Separator)+appServiceRelative) {
		return strings.TrimSuffix(unit, string(filepath.Separator)+appServiceRelative)
	}
	return ""
}

func installedAppService() (unit, daemon string) {
	if runtime.GOOS != "darwin" {
		return "", ""
	}
	if _, found, err := paths.PinnedService(); err != nil || !found {
		return "", ""
	}
	installed, err := daemonPath()
	if err != nil {
		return "", ""
	}
	app, err := pairedServiceApp(installed)
	if err != nil || app == "" {
		return "", ""
	}
	return filepath.Join(app, appServiceRelative), filepath.Join(app, "Contents", "MacOS", "dibd")
}

func checkAppService(ok reportFn, warn fixFn) {
	unit, _ := installedAppService()
	if unit == "" {
		return
	}
	if err := validateAppServicePayload(appForServiceUnit(unit)); err != nil {
		warn("Dibs background service payload is incomplete or invalid", err.Error())
		return
	}
	status, err := appServiceCall(appForServiceUnit(unit), "status")
	if err != nil {
		warn("cannot inspect Dibs background service", err.Error())
		return
	}
	switch status.Status {
	case "enabled":
		ok("Dibs background service is enabled")
	case "requires-approval":
		warn("Dibs background service requires approval", serviceApprovalError().Error())
	case "not-found", "not-registered":
		warn("Dibs background service is not registered", "run dibs configure --service to register the verified installed app; no enabled service is confirmed")
	default:
		warn("Dibs background service is "+status.Status, "run dibs configure --service to enable the installed "+
			"app's service")
	}
}
