// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const antigravityDibsGrant = "mcp(dibs/*)"

func antigravityConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "config"), nil
}

func trustAntigravityPlugin() error {
	dir, err := antigravityConfigDir()
	if err != nil {
		return err
	}
	changed, err := addAntigravityDibsGrant(filepath.Join(dir, "config.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("open Antigravity once to create its desktop profile, then run %s: %w",
				antigravityTrustCommand, err)
		}
		return fmt.Errorf("update Antigravity desktop permissions: %w", err)
	}
	if changed {
		fmt.Println("Added Antigravity allow rule " + antigravityDibsGrant + ".")
	} else {
		fmt.Println("Antigravity already has allow rule " + antigravityDibsGrant + ".")
	}
	return nil
}

type antigravityGrantConfig struct {
	root, settings, grants map[string]json.RawMessage
	allow                  []string
}

func decodeAntigravityGrants(data []byte) (antigravityGrantConfig, error) {
	var config antigravityGrantConfig
	if err := json.Unmarshal(data, &config.root); err != nil || config.root == nil {
		return config, fmt.Errorf("expected a JSON object in Antigravity settings")
	}
	config.settings = map[string]json.RawMessage{}
	if raw := config.root["userSettings"]; raw != nil {
		if err := json.Unmarshal(raw, &config.settings); err != nil || config.settings == nil {
			return config, fmt.Errorf("expected a JSON object in Antigravity userSettings")
		}
	}
	config.grants = map[string]json.RawMessage{}
	if raw := config.settings["globalPermissionGrants"]; raw != nil {
		if err := json.Unmarshal(raw, &config.grants); err != nil || config.grants == nil {
			return config, fmt.Errorf("expected a JSON object in Antigravity globalPermissionGrants")
		}
	}
	if raw := config.grants["allow"]; raw != nil {
		if err := json.Unmarshal(raw, &config.allow); err != nil {
			return config, fmt.Errorf("expected an array of strings in Antigravity allow rules")
		}
	}
	return config, nil
}

func addAntigravityDibsGrant(path string) (bool, error) {
	settings, err := readAntigravitySettings(path)
	if err != nil {
		return false, err
	}
	config, err := decodeAntigravityGrants(settings.data)
	if err != nil || slices.Contains(config.allow, antigravityDibsGrant) {
		return false, err
	}
	// These values are JSON validated on read and contain no custom encoders.
	config.grants["allow"], _ = json.Marshal(append(config.allow, antigravityDibsGrant))
	config.settings["globalPermissionGrants"], _ = json.Marshal(config.grants)
	config.root["userSettings"], _ = json.Marshal(config.settings)
	updated, err := json.MarshalIndent(config.root, "", "  ")
	if err != nil {
		return false, err
	}
	if err := settings.replace(append(updated, '\n')); err != nil {
		return false, err
	}
	return true, nil
}

type antigravitySettingsFile struct {
	path string
	mode os.FileMode
	data []byte
}

func readAntigravitySettings(path string) (antigravitySettingsFile, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return antigravitySettingsFile{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return antigravitySettingsFile{}, err
	}
	if !info.Mode().IsRegular() {
		return antigravitySettingsFile{}, fmt.Errorf("expected a regular file for Antigravity settings")
	}
	// #nosec G304 -- derived from the operator's Antigravity desktop config path
	data, err := os.ReadFile(target)
	return antigravitySettingsFile{target, info.Mode().Perm(), data}, err
}

func (f antigravitySettingsFile) replace(updated []byte) error {
	return replaceAntigravitySettings(f.path, f.mode, f.data, updated)
}

func replaceAntigravitySettings(path string, mode os.FileMode, original, updated []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".dibs-antigravity-")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }() // #nosec G703 -- own temporary settings file
	if _, err := f.Write(updated); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// #nosec G304 -- recheck the same resolved user settings file before replacement
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, original) {
		return fmt.Errorf("retry because Antigravity settings changed during this command")
	}
	// #nosec G703 -- replace the selected desktop settings atomically
	return os.Rename(f.Name(), path)
}

func (d *diagnosis) checkAntigravityGrants() {
	dir, err := antigravityConfigDir()
	if err != nil {
		return
	}
	manual := hasAntigravityDibsServer(filepath.Join(dir, "mcp_config.json"))
	native := hasAntigravityDibsServer(antigravityPluginServerPath(dir))
	if manual && native {
		d.warn("Antigravity has Dibs MCP servers in both the profile and the native plugin", antigravityPluginUsage)
	}
	if !manual && !native {
		return
	}
	// #nosec G304 -- read only the local Antigravity desktop settings
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		d.warn("Antigravity desktop permissions could not be read", "open Antigravity's permission settings")
		return
	}
	config, err := decodeAntigravityGrants(data)
	if err != nil {
		d.warn("Antigravity desktop permissions could not be parsed", "open Antigravity's permission settings")
	} else if !slices.Contains(config.allow, antigravityDibsGrant) {
		d.warn("no server-scoped Dibs MCP grant in Antigravity desktop settings", antigravityTrustCommand)
	}
}

func hasAntigravityDibsServer(path string) bool {
	server, err := readAntigravityDibsServer(path)
	return err == nil && server != nil
}

func readAntigravityDibsServer(path string) (json.RawMessage, error) {
	file, err := readAntigravitySettings(path)
	if err != nil {
		return nil, err
	}
	_, servers, err := decodeAntigravityServers(file.data)
	return servers["dibs"], err
}
