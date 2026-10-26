// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// SMAppService.notFound also describes an intact app before its first
// registration. Prove the sealed payload independently, before any stop or
// registration, rather than treating that registry status as a file check.
func validateAppServicePayload(app string) error {
	service, err := appServiceDictionary(filepath.Join(app, appServiceRelative))
	if err != nil {
		return fmt.Errorf("Dibs.app embedded service plist is missing or invalid; reinstall the complete distribution; nothing has been stopped: %w", err)
	}
	info, err := appServiceDictionary(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return fmt.Errorf("Dibs.app bundle metadata is invalid; nothing has been stopped: %w", err)
	}
	id, _ := info["CFBundleIdentifier"].(string)
	if id == "" || service["Label"] != appServiceLabel || service["BundleProgram"] != "Contents/MacOS/dibd" ||
		!reflect.DeepEqual(service["ProgramArguments"], []any{"Contents/MacOS/dibd", "-app-service"}) ||
		!reflect.DeepEqual(service["AssociatedBundleIdentifiers"], []any{id}) {
		return errors.New("Dibs.app embedded service plist has an invalid label, program, arguments or parent bundle; reinstall the complete distribution; nothing has been stopped")
	}
	return nil
}

func appServiceDictionary(file string) (map[string]any, error) {
	// #nosec G703 -- fixed payload paths inside the operator's paired app.
	info, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("service metadata must be a regular plist smaller than 1 MiB")
	}
	// #nosec G304,G703 -- same validated payload path, not participant input.
	body, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	root, err := readPolicyPlist(body)
	if err != nil {
		return nil, err
	}
	value, err := servicePlistValue(root)
	if err != nil {
		return nil, err
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("service metadata must be one plist dictionary")
	}
	return fields, nil
}
