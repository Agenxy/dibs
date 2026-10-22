// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"time"

	"github.com/agenxy/dibs/internal/machoidentity"
	"github.com/agenxy/dibs/internal/paths"
)

const appServiceLabel = "org.agenxy.dibs.daemon"
const appServiceRelative = "Contents/Library/LaunchAgents/org.agenxy.dibs.daemon.plist"

type appServicePlan struct {
	app, dir, legacy  string
	legacyBody        []byte
	path              *string
	pathSource        string
	needsRegistration bool
}

type appServiceStatus struct {
	Status           string `json:"status"`
	ApprovalRequired bool   `json:"approval_required"`
}

// Locate the app paired with this installed daemon. Homebrew moves its app
// into Applications; accept that location only for a Caskroom entry point and
// only after comparing the executable sections, never merely its version.
func pairedServiceApp(installed string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", nil
	}
	return machoidentity.PairedApp(installed)
}

func appServiceCall(app, mode string) (appServiceStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// #nosec G204 -- validated installed app and one fixed service mode, no shell
	cmd := exec.CommandContext(ctx, filepath.Join(app, "Contents", "MacOS", "dibs-notify"), "--service-"+mode)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return appServiceStatus{}, fmt.Errorf("Dibs app service %s: %w: %s", mode, err, out)
	}
	var status appServiceStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return status, fmt.Errorf("Dibs app service returned an unreadable status: %w", err)
	}
	switch status.Status {
	case "enabled", "not-registered", "requires-approval", "not-found":
	default:
		return status, fmt.Errorf("Dibs app service returned unknown status %q", status.Status)
	}
	return status, nil
}

func serviceApprovalError() error {
	return errors.New("Dibs needs background-item approval: System Settings → General → Login Items & Extensions → Allow in the Background → Dibs; approve it, then run dibs configure --service again. Requested is not enabled")
}

// The old unit is operator configuration. Carry only settings the new service
// actually understands; name every refusal BEFORE stopping the existing job.
func readLegacyAppService(file string) (*appServicePlan, error) {
	body, err := os.ReadFile(file) // #nosec G304 -- fixed user's legacy service path
	if err != nil {
		return nil, err
	}
	if len(body) > 1<<20 {
		return nil, errors.New("legacy LaunchAgent exceeds 1 MiB")
	}
	root, err := readPolicyPlist(body)
	if err != nil {
		return nil, fmt.Errorf("read legacy LaunchAgent (convert a binary plist with plutil -convert xml1): %w", err)
	}
	value, err := servicePlistValue(root)
	if err != nil {
		return nil, err
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("legacy LaunchAgent must have one dictionary")
	}
	allowed := map[string]bool{"Label": true, "ProgramArguments": true, "EnvironmentVariables": true, "RunAtLoad": true, "KeepAlive": true, "StandardOutPath": true, "StandardErrorPath": true, "ProcessType": true}
	for key := range fields {
		if !allowed[key] {
			return nil, fmt.Errorf("unsupported legacy service setting %s; preserve the running service and move this setting into supported Dibs configuration before retrying", key)
		}
	}
	if fields["Label"] != "org.agenxy.dibs" {
		return nil, fmt.Errorf("unsupported legacy Label %v", fields["Label"])
	}
	args, ok := fields["ProgramArguments"].([]any)
	if !ok || len(args) != 3 || args[1] != "-dir" {
		return nil, fmt.Errorf("unsupported legacy ProgramArguments %v; only the existing -dir argument migrates; move other flags into dibs.toml before retrying", fields["ProgramArguments"])
	}
	dir, ok := args[2].(string)
	if !ok || !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("legacy -dir must be absolute: %v", args[2])
	}
	for key, want := range map[string]any{"RunAtLoad": true, "KeepAlive": map[string]any{"SuccessfulExit": false}} {
		if v, exists := fields[key]; exists && !reflect.DeepEqual(v, want) {
			return nil, fmt.Errorf("unsupported legacy %s=%v; the app service uses %v, so nothing has been stopped", key, v, want)
		}
	}
	if v, exists := fields["ProcessType"]; exists && v != "Interactive" && v != "Background" {
		return nil, fmt.Errorf("unsupported legacy ProcessType=%v", v)
	}
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		if v, exists := fields[key]; exists && v != filepath.Join(dir, "dibd.log") {
			return nil, fmt.Errorf("unsupported legacy %s=%v; Dibs.app writes %s/dibd.log", key, v, dir)
		}
	}
	p := &appServicePlan{dir: dir, legacy: file, legacyBody: body}
	if value, exists := fields["EnvironmentVariables"]; exists {
		env, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("legacy EnvironmentVariables must be a dictionary")
		}
		for key, value := range env {
			if key != "PATH" {
				return nil, fmt.Errorf("unsupported legacy environment %s; move it into supported Dibs configuration before retrying; the legacy job is untouched", key)
			}
			path, ok := value.(string)
			if !ok {
				return nil, errors.New("legacy PATH must be a string")
			}
			p.path, p.pathSource = &path, "migrated"
		}
	}
	return p, nil
}

func servicePlistValue(n *policyElement) (any, error) {
	if n == nil {
		return nil, errors.New("missing service plist")
	}
	switch n.name {
	case "plist":
		if len(n.children) == 1 {
			return servicePlistValue(n.children[0])
		}
	case "string":
		if len(n.children) == 0 {
			return n.text, nil
		}
	case "true", "false":
		if len(n.children) == 0 {
			return n.name == "true", nil
		}
	case "array":
		values := []any{}
		for _, child := range n.children {
			value, err := servicePlistValue(child)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	case "dict":
		if len(n.children)%2 != 0 {
			break
		}
		values := map[string]any{}
		for i := 0; i < len(n.children); i += 2 {
			key := n.children[i]
			if key.name != "key" || len(key.children) != 0 {
				return nil, errors.New("invalid service plist key")
			}
			if _, exists := values[key.text]; exists {
				return nil, fmt.Errorf("duplicate service plist key %q", key.text)
			}
			value, err := servicePlistValue(n.children[i+1])
			if err != nil {
				return nil, err
			}
			values[key.text] = value
		}
		return values, nil
	}
	return nil, fmt.Errorf("unsupported service plist element %s", n.name)
}

func prepareAppService(installed, selected string) (*appServicePlan, error) {
	app, err := pairedServiceApp(installed)
	if err != nil || app == "" {
		return nil, err
	}
	p := &appServicePlan{app: app, dir: selected}
	legacy := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "org.agenxy.dibs.plist")
	if _, err := os.Lstat(legacy); err == nil {
		p, err = readLegacyAppService(legacy)
		if err != nil {
			return nil, fmt.Errorf("cannot migrate %s; nothing has been stopped: %w", legacy, err)
		}
		p.app = app
		if os.Getenv("DIBS_DIR") != "" && filepath.Clean(selected) != filepath.Clean(p.dir) {
			return nil, fmt.Errorf("legacy service belongs to %s, not requested %s; it was left untouched", p.dir, selected)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	pin, found, err := paths.PinnedService()
	if err != nil && os.Getenv("DIBS_DIR") == "" {
		return nil, err
	}
	if found {
		if filepath.Clean(pin.Dir) != filepath.Clean(p.dir) {
			return nil, fmt.Errorf("Dibs app service is pinned to %s, not %s; refusing to repoint another board's service", pin.Dir, p.dir)
		}
		if pin.ServicePathSet {
			path := pin.ServicePath
			p.path, p.pathSource = &path, pin.ServicePathSource
		}
	} else if p.legacy == "" {
		if path, exists := os.LookupEnv("PATH"); exists {
			p.path, p.pathSource = &path, "configured"
		}
	}
	if err := p.preflight(); err != nil {
		return nil, err
	}
	return p, nil
}

// Upgrading a manually started board does not install a login service. Only
// an existing service belonging to this board participates in migration.
func prepareAppUpgrade(installed, selected string) (*appServicePlan, error) {
	if runtime.GOOS != "darwin" {
		return nil, nil
	}
	pin, found, err := paths.PinnedService()
	if err != nil {
		return nil, err
	}
	if found {
		if filepath.Clean(pin.Dir) != filepath.Clean(selected) {
			return nil, nil
		}
		return prepareAppService(installed, selected)
	}
	legacy := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "org.agenxy.dibs.plist")
	if _, err := os.Lstat(legacy); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if os.Getenv("DIBS_DIR") != "" && !unitNames(legacy, selected) {
		return nil, nil
	}
	return prepareAppService(installed, selected)
}

func (p *appServicePlan) preflight() error {
	// Verify nested code and the enclosing app before it can own the service.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// #nosec G204 -- own installed app, fixed codesign mode, no shell
	if out, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", p.app).CombinedOutput(); err != nil {
		return fmt.Errorf("invalid installed Dibs.app; nothing has been stopped: %w: %s", err, out)
	}
	status, err := appServiceCall(p.app, "status")
	if err != nil {
		return err
	}
	if status.ApprovalRequired || status.Status == "requires-approval" {
		return serviceApprovalError()
	}
	if status.Status == "not-found" {
		return errors.New("Dibs.app has no usable embedded service; reinstall the complete distribution")
	}
	p.needsRegistration = status.Status != "enabled"
	if p.legacy != "" && status.Status == "enabled" {
		return errors.New("both legacy and app service registrations exist; run dibs doctor and remove the obsolete registration before migration")
	}
	if p.legacy != "" {
		current, err := os.ReadFile(p.legacy)
		if err != nil {
			return err
		} // #nosec G304 -- previously validated legacy unit
		if !bytes.Equal(current, p.legacyBody) {
			return errors.New("legacy service changed after planning; run the command again; nothing has been stopped")
		}
		if err := unitIsWritable(p.legacy); err != nil {
			return err
		}
	}
	return nil
}

func (p *appServicePlan) writePin(dir string) error {
	return paths.WriteServiceSettings(dir, p.path, p.pathSource)
}

func (p *appServicePlan) start() error {
	status, err := appServiceCall(p.app, "register")
	if err != nil {
		return err
	}
	if status.ApprovalRequired || status.Status == "requires-approval" {
		return serviceApprovalError()
	}
	if status.Status != "enabled" {
		return fmt.Errorf("Dibs app service registration returned %s; no enabled service is confirmed", status.Status)
	}
	return nil
}

// configureAppService is explicit installation authority. It never runs from
// doctor, a bridge call, or merely discovering that a service is missing.
func configureAppService(installed, selected string) (bool, error) {
	p, err := prepareAppService(installed, selected)
	if err != nil || p == nil {
		return p != nil || err != nil, err
	}
	if p.legacy != "" {
		// The upgrade path proves replay and preserves fleet evidence before it
		// reaches the same migration. Reuse it for an existing legacy service.
		return true, upgrade(upgradeOpts{})
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return true, err
	}
	if err := p.writePin(p.dir); err != nil {
		return true, err
	}
	if err := p.start(); err != nil {
		return true, err
	}
	fmt.Println("Dibs background service is enabled; its board directory is " + p.dir)
	requestInstalledFolderAccess()
	return true, nil
}

func requestInstalledFolderAccess() {
	if runtime.GOOS != "darwin" {
		return
	}
	if _, err := requestFolderAccess(); err != nil {
		fmt.Fprintln(os.Stderr, "folder access was not requested:", err, "; run dibs doctor --request-folder-access when the daemon is serving")
		return
	}
	fmt.Println("Desktop, Documents and Downloads access requested from the daemon. Requested is not granted; check dibs doctor and System Settings → Privacy & Security → Files & Folders → Dibs.")
}
