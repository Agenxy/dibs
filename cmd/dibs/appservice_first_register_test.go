// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/paths"
)

// The native status is an external input; all registration is handled by the
// private executor fixture. Enter where configure --service enters, before
// the first registration, rather than calling register directly.
func TestConfigureFirstAppRegistrationAcceptsNotFoundWithValidPayload(t *testing.T) {
	f := newAppServiceTestFixture(t)
	installFirstRegistrationPlist(t, f)
	if err := os.WriteFile(f.state, []byte("not-found"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := serviceCommand(nil); err != nil {
		t.Fatalf("intact never-registered app refused: %v", err)
	}
	state, err := os.ReadFile(f.state)
	if err != nil || string(state) != "enabled" {
		t.Fatalf("registration not confirmed: %s %v", state, err)
	}
	pin, found, err := paths.PinnedService()
	if err != nil || !found || pin.Dir != f.dir {
		t.Fatalf("lost selected board: %+v %v %v", pin, found, err)
	}
	body, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"mode":"status"`) || !strings.Contains(string(body), `"mode":"register"`) {
		t.Fatalf("real preflight/register path not reached: %s", body)
	}
	if f.requests.Load() != 1 {
		t.Fatalf("permission request count=%d", f.requests.Load())
	}
}

func installFirstRegistrationPlist(t *testing.T, f *appServiceTestFixture) {
	t.Helper()
	file := filepath.Join(f.app, appServiceRelative)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fixtureAppServicePlist
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	resignFirstRegistrationFixture(t, f.app)
}

const fixtureAppServicePlist = `<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>org.agenxy.dibs.daemon</string><key>BundleProgram</key><string>Contents/MacOS/dibd</string><key>AssociatedBundleIdentifiers</key><array><string>org.agenxy.dibs.service-test</string></array><key>ProgramArguments</key><array><string>Contents/MacOS/dibd</string><string>-app-service</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict></dict></plist>`

func resignFirstRegistrationFixture(t *testing.T, app string) {
	t.Helper()
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--timestamp=none", app).CombinedOutput(); err != nil {
		t.Fatalf("sign setup: %v %s", err, out)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		t.Fatalf("verify setup: %v %s", err, out)
	}
}

func TestAppPreflightRefusesMissingServicePlistEvenWhenSignatureIsValid(t *testing.T) {
	f := newAppServiceTestFixture(t)
	// The signed app alone is insufficient: its embedded service is required
	// independently of the operating system's registration status.
	if err := os.Remove(filepath.Join(f.app, appServiceRelative)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	resignFirstRegistrationFixture(t, f.app)
	if err := os.WriteFile(f.state, []byte("not-found"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := serviceCommand(nil); err == nil {
		t.Fatal("missing plist was treated as first registration")
	}
	body, _ := os.ReadFile(f.trace)
	if strings.Contains(string(body), `"mode":"register"`) {
		t.Fatalf("incomplete payload reached register: %s", body)
	}
	if _, found, err := paths.PinnedService(); found || err != nil {
		t.Fatalf("invalid payload changed service pin: %v %v", found, err)
	}
}

func TestAppPreflightRefusesInvalidServiceMetadataBeforeRegistration(t *testing.T) {
	for _, change := range []struct{ old, new string }{
		{"<string>org.agenxy.dibs.daemon</string>", "<string>other.service</string>"},
		{"<string>Contents/MacOS/dibd</string>", "<string>Contents/MacOS/other</string>"},
		{"<string>-app-service</string>", "<string>-dir</string>"},
		{"<string>org.agenxy.dibs.service-test</string>", "<string>other.parent</string>"},
	} {
		t.Run(change.new, func(t *testing.T) {
			f := newAppServiceTestFixture(t)
			installFirstRegistrationPlist(t, f)
			bad := strings.ReplaceAll(fixtureAppServicePlist, change.old, change.new)
			if bad == fixtureAppServicePlist {
				t.Fatal("setup mutation did not change plist")
			}
			if err := os.WriteFile(filepath.Join(f.app, appServiceRelative), []byte(bad), 0o600); err != nil {
				t.Fatal(err)
			}
			resignFirstRegistrationFixture(t, f.app)
			if err := os.WriteFile(f.state, []byte("not-registered"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := serviceCommand(nil); err == nil {
				t.Fatal("invalid service metadata reached registration")
			}
			body, _ := os.ReadFile(f.trace)
			if strings.Contains(string(body), `"mode":"register"`) {
				t.Fatalf("invalid plist reached native mutation: %s", body)
			}
		})
	}
}

func TestUpgradePlansFirstRegistrationWithoutChangingTheService(t *testing.T) {
	f := newAppServiceTestFixture(t)
	f.legacy(t, "")
	if err := os.WriteFile(f.state, []byte("not-found"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := prepareAppUpgrade(f.root, f.dir)
	if err != nil || p == nil || !p.needsRegistration || p.dir != f.dir {
		t.Fatalf("first upgrade plan: %+v %v", p, err)
	}
	body, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"mode":"register"`) || strings.Contains(string(body), `"mode":"unregister"`) {
		t.Fatalf("planning changed service: %s", body)
	}
}

func TestDoctorExplainsFirstRegistrationWithoutChangingIt(t *testing.T) {
	f := newAppServiceTestFixture(t)
	if err := paths.WriteServiceDirectory(f.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.state, []byte("not-found"), 0o600); err != nil {
		t.Fatal(err)
	}
	var what, fix string
	checkAppService(func(string) { t.Fatal("never-registered service reported enabled") }, func(w, f string) { what, fix = w, f })
	if what != "Dibs background service is not registered" || !strings.Contains(fix, "configure --service") {
		t.Fatalf("doctor: %s %s", what, fix)
	}
	body, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"mode":"register"`) || strings.Contains(string(body), `"mode":"unregister"`) {
		t.Fatalf("doctor changed service: %s", body)
	}
}

func TestNeverRegisteredServiceRemovalRequiresPositiveJobAbsence(t *testing.T) {
	for _, mode := range []string{"absent", "loaded", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f := newAppServiceTestFixture(t)
			if err := os.WriteFile(f.state, []byte("not-found"), 0o600); err != nil {
				t.Fatal(err)
			}
			previous := readAppServiceJob
			t.Cleanup(func() { readAppServiceJob = previous })
			readAppServiceJob = func() ([]byte, error) {
				switch mode {
				case "absent":
					return nil, errAppServiceNotLoaded
				case "loaded":
					return []byte("a loaded job"), nil
				default:
					return nil, errors.New("probe unavailable")
				}
			}
			err := serviceCommand([]string{"--remove"})
			if (err == nil) != (mode == "absent") {
				t.Fatalf("job=%s removal=%v", mode, err)
			}
			body, _ := os.ReadFile(f.trace)
			if strings.Contains(string(body), `"mode":"unregister"`) {
				t.Fatalf("never-registered service reached the refusing native unregister: %s", body)
			}
		})
	}
}
