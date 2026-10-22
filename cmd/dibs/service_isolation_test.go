// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

var (
	operatorHomeForTest  = os.Getenv("HOME")
	defaultTestDaemon    string
	serviceFixtureRoot   string
	serviceFixtureSource string
	serviceFixtureOnce   sync.Once
	serviceFixtureBytes  []byte
	serviceFixtureErr    error
)

func isolateCLIServiceHome(root string) {
	serviceFixtureRoot = root
	serviceFixtureSource, _ = os.Getwd()
	executeAppServiceCommand = func(cmd *exec.Cmd) ([]byte, error) {
		if os.Getenv("DIBS_TEST_APP_SERVICE_HELPER") != "1" || !fixtureServicePath(cmd.Path) ||
			!fixtureServicePath(os.Getenv("DIBS_TEST_APP_SERVICE_STATE")) {
			panic(fmt.Sprintf("unfaked app service command in CLI test: %s", cmd.Path))
		}
		return cmd.CombinedOutput()
	}
	privateDir := fixtureServicePath(os.Getenv("DIBS_DIR"))
	if fixtureServicePath(os.Getenv("HOME")) && privateDir {
		return // a nested fixture already supplied its isolated identity and board
	}

	// Keep Go's existing caches available when nested build fixtures change HOME.
	if goPath, lookupErr := exec.LookPath("go"); lookupErr == nil && operatorHomeForTest != "" {
		for _, key := range []string{"GOCACHE", "GOMODCACHE"} {
			if os.Getenv(key) != "" {
				continue
			}
			out, runErr := exec.Command(goPath, "env", key).Output()
			if runErr != nil {
				panic(runErr)
			}
			if err := os.Setenv(key, strings.TrimSpace(string(out))); err != nil {
				panic(err)
			}
		}
	}
	home, bin := filepath.Join(root, "home"), filepath.Join(root, "bin")
	if err := os.MkdirAll(home, 0o700); err != nil {
		panic(err)
	}
	if err := os.MkdirAll(bin, 0o700); err != nil {
		panic(err)
	}
	defaultTestDaemon = filepath.Join(bin, "dibd")
	if err := os.WriteFile(defaultTestDaemon, nil, 0o700); err != nil {
		panic(err)
	}
	settings := map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config")}
	if !privateDir {
		settings["DIBS_DIR"] = filepath.Join(home, "board")
		settings["DIBS_ADDR"] = "http://127.0.0.1:1"
		settings["PATH"] = bin + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	for key, value := range settings {
		if err := os.Setenv(key, value); err != nil {
			panic(err)
		}
	}
}

func fixtureServicePath(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return false
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, canonical)
	return err == nil && filepath.IsLocal(rel)
}

func TestServiceLookupsStayInsideTheTestHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == operatorHomeForTest || !fixtureServicePath(home) {
		t.Fatal("service tests resolved the operator's real home")
	}
	pin, err := paths.ServiceConfigPath()
	if err != nil || pin != filepath.Join(home, "Library", "Application Support", "org.agenxy.dibs", "service.toml") {
		t.Fatalf("service pin escaped isolated home: %s %v", pin, err)
	}
	daemon, err := daemonPath()
	if err != nil || daemon != defaultTestDaemon {
		t.Fatalf("installed daemon lookup escaped isolated fixture: %s %v", daemon, err)
	}
	unit, _ := unitDaemon()
	if unit != "" {
		rel, err := filepath.Rel(home, unit)
		if err != nil || !filepath.IsLocal(rel) {
			t.Fatal("service unit escaped isolated home:", unit)
		}
	}
}

// ServiceManagement is a native external helper, not Go code under test. Use
// an uninstrumented fixture process so each status call does not inherit the
// Go race runtime's one-second exit delay. The CLI and its transaction driver
// remain race-instrumented; no gate or race detector option is changed.
func serviceFixtureImage(t *testing.T) ([]byte, error) {
	t.Helper()
	serviceFixtureOnce.Do(func() {
		image := filepath.Join(serviceFixtureRoot, "app-service-fixture")
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "test", "-c", "-race=false", "-o", image, ".")
		cmd.Dir = serviceFixtureSource
		output, err := cmd.CombinedOutput()
		if err != nil {
			serviceFixtureErr = fmt.Errorf("build external service fixture: %w: %s", err, output)
			return
		}
		serviceFixtureBytes, serviceFixtureErr = os.ReadFile(image)
	})
	return serviceFixtureBytes, serviceFixtureErr
}

func TestAppServiceExecutorRefusesUnfakedHelpers(t *testing.T) {
	t.Setenv("DIBS_TEST_APP_SERVICE_HELPER", "")
	defer func() {
		if recover() == nil {
			t.Fatal("an unfaked app service command reached external execution")
		}
	}()
	_, _ = executeAppServiceCommand(exec.Command("/nonexistent/unfaked-app-service"))
}
