// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

// This private executable records ServiceManagement messages. It NEVER calls
// ServiceManagement, launchctl, notifications or a consent API. Transaction
// tests separately substitute the legacy supervisor boundary as well.
func appServiceFixture() int {
	if len(os.Args) != 2 {
		return 11
	}
	mode := strings.TrimPrefix(os.Args[1], "--service-")
	stateFile := os.Getenv("DIBS_TEST_APP_SERVICE_STATE")
	state, err := os.ReadFile(stateFile)
	if err != nil {
		return 12
	}
	status := strings.TrimSpace(string(state))
	switch mode {
	case "status":
	case "register":
		status = os.Getenv("DIBS_TEST_APP_SERVICE_RESULT")
		if status == "" {
			status = "enabled"
		}
	case "unregister":
		status = "not-registered"
	default:
		return 13
	}
	f, err := os.OpenFile(os.Getenv("DIBS_TEST_APP_SERVICE_TRACE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 14
	}
	selected, found, pinErr := paths.PinnedService()
	err = json.NewEncoder(f).Encode(map[string]any{"mode": mode, "status": status, "pin_found": found, "pin_error": fmt.Sprint(pinErr), "dir": selected.Dir, "path": selected.ServicePath, "source": selected.ServicePathSource})
	_ = f.Close()
	if err != nil {
		return 15
	}
	if status == "fail" {
		_, _ = os.Stderr.WriteString("fixture registration refused")
		return 16
	}
	if err := os.WriteFile(stateFile, []byte(status), 0o600); err != nil {
		return 17
	}
	if err := json.NewEncoder(os.Stdout).Encode(appServiceStatus{status, status == "requires-approval"}); err != nil {
		return 18
	}
	return 0
}

type appServiceTestFixture struct {
	home, dir, root, app, state, trace string
	requests                           atomic.Int64
}

func newAppServiceTestFixture(t *testing.T) *appServiceTestFixture {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("native signed app fixture")
	}
	f := &appServiceTestFixture{home: t.TempDir()}
	f.dir = filepath.Join(f.home, "board")
	f.root = filepath.Join(f.home, "bin", "dibd")
	f.app = filepath.Join(filepath.Dir(f.root), "Dibs.app")
	f.state = filepath.Join(f.home, "state")
	f.trace = filepath.Join(f.home, "trace")
	macos := filepath.Join(f.app, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []string{f.root, filepath.Join(macos, "dibd"), filepath.Join(macos, "dibs-notify")} {
		if err := os.WriteFile(at, image, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>org.agenxy.dibs.service-test</string><key>CFBundleExecutable</key><string>dibs-notify</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(f.app, "Contents", "Info.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, at := range []string{filepath.Join(macos, "dibd"), f.app} {
		if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--timestamp=none", at).CombinedOutput(); err != nil {
			t.Fatalf("fixture signing: %v %s", err, out)
		}
	}
	for name, body := range map[string]string{f.state: "not-registered", filepath.Join(f.dir, "local.secret"): strings.Repeat("a", 64), filepath.Join(f.dir, "node_id"): "app-service-fixture"} {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", f.home)
	t.Setenv("CODEX_HOME", filepath.Join(f.home, ".codex"))
	t.Setenv("DIBS_DIR", f.dir)
	t.Setenv("PATH", filepath.Dir(f.root)+":/usr/bin:/bin")
	t.Setenv("DIBS_ADDR", "")
	t.Setenv("DIBS_TEST_APP_SERVICE_HELPER", "1")
	t.Setenv("DIBS_TEST_APP_SERVICE_STATE", f.state)
	t.Setenv("DIBS_TEST_APP_SERVICE_TRACE", f.trace)
	t.Setenv("DIBS_TEST_APP_SERVICE_RESULT", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/board":
			_, _ = w.Write([]byte(`{"node":"app-service-fixture","agents":[],"serial":7}`))
		case "/mcp":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"version":"0.0.16"}}}`))
		case "/api/folder-access/request":
			if r.Method != http.MethodPost || r.Header.Get("X-Dibs-Local") != strings.Repeat("a", 64) {
				t.Error("request was not authenticated POST")
			}
			f.requests.Add(1)
			_, _ = w.Write([]byte(`{"requested":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DIBS_TEST_APP_SERVICE_ADDR", srv.Listener.Addr().String())
	if err := os.WriteFile(filepath.Join(f.dir, "dibs.toml"), []byte("addr = '"+srv.Listener.Addr().String()+"'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func appServiceRegistryFixture() int {
	release, err := paths.Claim(paths.Daemon{PID: os.Getpid(), Addr: os.Getenv("DIBS_TEST_APP_SERVICE_ADDR"), Dir: os.Getenv("DIBS_DIR"), Scheme: "http"}, true)
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error())
		return 20
	}
	defer release()
	_, _ = os.Stdout.WriteString("ready\n")
	_, err = io.Copy(io.Discard, os.Stdin)
	if err != nil {
		return 21
	}
	return 0
}

func (f *appServiceTestFixture) liveManual(t *testing.T) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	cmd := exec.CommandContext(ctx, f.root, "--app-service-registry")
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = input.Close(); cancel(); <-done })
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("live registry setup: %q %v", line, err)
	}
	if live := runningDaemon(f.dir); hostPort(live.addr) != os.Getenv("DIBS_TEST_APP_SERVICE_ADDR") {
		t.Fatal("setup did not publish live registry:", live)
	}
	return done
}

func TestConfigureMovesALiveManualBoardAndRecoversItsMode(t *testing.T) {
	for _, outcome := range []string{"enabled", "requires-approval"} {
		t.Run(outcome, func(t *testing.T) {
			f := newAppServiceTestFixture(t)
			stopped := f.liveManual(t)
			t.Setenv("DIBS_TEST_APP_SERVICE_RESULT", outcome)
			var recoveryCalls int
			previous := startManualAppRecovery
			t.Cleanup(func() { startManualAppRecovery = previous })
			startManualAppRecovery = func(installed, dir, unit string, was daemonState) error {
				recoveryCalls++
				if installed != f.root || dir != f.dir || unit != "" || hostPort(was.addr) != os.Getenv("DIBS_TEST_APP_SERVICE_ADDR") {
					t.Fatalf("manual recovery changed original mode: %s %s %s %+v", installed, dir, unit, was)
				}
				state, err := os.ReadFile(f.state)
				if err != nil || string(state) != "not-registered" {
					t.Fatal("manual recovery preceded app removal")
				}
				return nil
			}
			err := serviceCommand(nil)
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("old private manual daemon was not stopped")
			}
			if outcome == "enabled" {
				if err != nil || recoveryCalls != 0 || f.requests.Load() != 1 {
					t.Fatalf("manual cutover: %v recovery=%d requests=%d", err, recoveryCalls, f.requests.Load())
				}
			} else {
				if err == nil || recoveryCalls != 1 || f.requests.Load() != 0 {
					t.Fatalf("refused cutover did not restore manual mode: %v recovery=%d requests=%d", err, recoveryCalls, f.requests.Load())
				}
				if _, found, err := paths.PinnedService(); err != nil || found {
					t.Fatalf("failed manual migration left new pin: %t %v", found, err)
				}
			}
		})
	}
}

func TestAppMigrationDryRunCreatesNoServicePinDirectory(t *testing.T) {
	f := newAppServiceTestFixture(t)
	live := f.liveManual(t)
	f.legacy(t, "/bin:/usr/bin")
	pin, err := paths.ServiceConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(pin)); !os.IsNotExist(err) {
		t.Fatal("setup: pin directory already exists:", err)
	}
	if err := upgrade(upgradeOpts{dryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(pin)); !os.IsNotExist(err) {
		t.Fatal("dry run created service pin directory:", err)
	}
	select {
	case <-live:
		t.Fatal("dry run stopped the original process")
	default:
	}
	trace, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(trace), `"mode":"register"`) || strings.Contains(string(trace), `"mode":"unregister"`) || f.requests.Load() != 0 {
		t.Fatalf("dry run performed service/permission changes: %s", trace)
	}
}

func TestConfigureRepairsAnExplicitBrokenPinWithoutGuessingAMissingOne(t *testing.T) {
	t.Run("explicit-repair", func(t *testing.T) {
		f := newAppServiceTestFixture(t)
		pin, err := paths.ServiceConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(pin), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pin, []byte("version=999\ndata_dir='/wrong-board'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.state, []byte("enabled"), 0o600); err != nil {
			t.Fatal(err)
		}
		stopped := f.liveManual(t)
		if err := serviceCommand(nil); err != nil {
			t.Fatal("authorized explicit pin repair:", err)
		}
		selected, found, err := paths.PinnedService()
		if err != nil || !found || selected.Dir != f.dir {
			t.Fatalf("pin was not repaired: %+v %t %v", selected, found, err)
		}
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("repair did not cut over old owned fixture")
		}
	})
	t.Run("missing-needs-explicit-directory", func(t *testing.T) {
		f := newAppServiceTestFixture(t)
		t.Setenv("DIBS_DIR", "")
		if err := os.WriteFile(f.state, []byte("enabled"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := serviceCommand(nil); err == nil || !strings.Contains(err.Error(), "set DIBS_DIR") {
			t.Fatalf("enabled service without a pin guessed a directory: %v", err)
		}
		if _, err := os.Stat(filepath.Join(f.home, ".dibs")); !os.IsNotExist(err) {
			t.Fatal("missing pin created default board:", err)
		}
		if _, found, err := paths.PinnedService(); err != nil || found {
			t.Fatalf("missing pin was guessed: %t %v", found, err)
		}
	})
}

func (f *appServiceTestFixture) legacy(t *testing.T, path string) string {
	t.Helper()
	if err := writeLaunchAgent(f.root, f.dir); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.home, "Library", "LaunchAgents", "org.agenxy.dibs.plist")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve arbitrary PATH literally, including an explicitly empty one.
	a := strings.Index(string(body), "<key>PATH</key><string>")
	if a < 0 {
		t.Fatal("legacy setup omitted PATH")
	}
	start := a + len("<key>PATH</key><string>")
	end := start + strings.Index(string(body[start:]), "</string>")
	body = append(append(append([]byte{}, body[:start]...), []byte(xmlText(path))...), body[end:]...)
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestConfigureAppServicePinsBeforeRegistrationAndRequestsOnce(t *testing.T) {
	f := newAppServiceTestFixture(t)
	var foreignCalls atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignCalls.Add(1)
		http.Error(w, "wrong board", http.StatusForbidden)
	}))
	t.Cleanup(foreign.Close)
	t.Setenv("DIBS_ADDR", foreign.URL)
	if err := serviceCommand(nil); err != nil {
		t.Fatal(err)
	}
	pin, found, err := paths.PinnedService()
	if err != nil || !found || pin.Dir != f.dir || pin.ServicePath != os.Getenv("PATH") || pin.ServicePathSource != "configured" {
		t.Fatalf("pin: %+v %t %v", pin, found, err)
	}
	if f.requests.Load() != 1 {
		t.Fatalf("acquisition attempts=%d, want one", f.requests.Load())
	}
	if foreignCalls.Load() != 0 {
		t.Fatal("installation used the CLI's foreign DIBS_ADDR")
	}
	body, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"mode":"register","path":`) || !strings.Contains(string(body), `"pin_found":true`) {
		t.Fatalf("registration did not read final pin: %s", body)
	}
}

func TestAppServiceRemovalKeepsTheBoardAndRefusesAnotherBoard(t *testing.T) {
	f := newAppServiceTestFixture(t)
	if err := paths.WriteServiceDirectory(f.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.state, []byte("enabled"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_DIR", filepath.Join(f.home, "other-board"))
	if err := serviceCommand([]string{"--remove"}); err == nil || !strings.Contains(err.Error(), "left untouched") {
		t.Fatalf("foreign removal: %v", err)
	}
	if state, err := os.ReadFile(f.state); err != nil || string(state) != "enabled" {
		t.Fatal("foreign removal changed service")
	}
	t.Setenv("DIBS_DIR", f.dir)
	if err := serviceCommand([]string{"--remove"}); err != nil {
		t.Fatal(err)
	}
	if state, err := os.ReadFile(f.state); err != nil || string(state) != "not-registered" {
		t.Fatal("service was not unregistered")
	}
	if _, found, err := paths.PinnedService(); err != nil || !found {
		t.Fatalf("removal lost board pin: %t %v", found, err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "node_id")); err != nil {
		t.Fatal("removal lost board data:", err)
	}
}

func TestConfigureCurrentAppServiceIsANoOp(t *testing.T) {
	f := newAppServiceTestFixture(t)
	if err := serviceCommand(nil); err != nil {
		t.Fatal("first registration:", err)
	}
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(filepath.Dir(f.root), "dibs-service-probe")
	if err := os.WriteFile(probe, image, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, probe, "-test.run=^TestCurrentAppServiceConfigureDriver$")
	cmd.Env = append(os.Environ(), "DIBS_TEST_APP_SERVICE_CONFIGURE_DRIVER=1")
	output, err := cmd.CombinedOutput()
	out := string(output)
	if err != nil || !strings.Contains(out, "nothing to do") {
		t.Fatalf("repeated current configuration: %v %s", err, out)
	}
	trace, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(trace), `"mode":"register"`) != 1 || strings.Contains(string(trace), `"mode":"unregister"`) || f.requests.Load() != 1 {
		t.Fatalf("current postflight repeated mutations or acquisition: requests=%d trace=%s", f.requests.Load(), trace)
	}
}

func TestCurrentAppServiceConfigureDriver(t *testing.T) {
	if os.Getenv("DIBS_TEST_APP_SERVICE_CONFIGURE_DRIVER") != "1" {
		return
	}
	// The real registry is required by upgrade's target selection. A private
	// child owns the fixture PID, so a regression can never signal the parent
	// test runner or a real daemon. Its HOME contains only this fixture entry.
	release, err := paths.Claim(paths.Daemon{PID: os.Getpid(), Addr: os.Getenv("DIBS_TEST_APP_SERVICE_ADDR"), Dir: os.Getenv("DIBS_DIR"), Scheme: "http"}, true)
	if err != nil {
		t.Fatal("registry setup:", err)
	}
	defer release()
	if err := serviceCommand(nil); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorFindsOnlyThisUsersEnabledOrphanedDibsRegistration(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		enabled, otherUser, present, foreign bool
		want                                 bool
	}{
		{"orphan", true, false, false, false, true},
		{"history", false, false, false, false, false},
		{"other-user", true, true, false, false, false},
		{"present", true, false, true, false, false},
		{"different-app-named-Dibs", true, false, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAppServiceTestFixture(t)
			uid := os.Getuid()
			if tc.otherUser {
				uid++
			}
			app := filepath.Join(f.home, "removed", "Dibs.app")
			if tc.present {
				app = f.app
			}
			id := "org.agenxy.dibs"
			if tc.foreign {
				id = "com.example.other"
			}
			disposition := "disabled"
			if tc.enabled {
				disposition = "enabled"
			}
			body := fmt.Sprintf("Records for UID %d : fixture\n UUID: parent\n Name: Dibs\n Identifier: 2.%s\n Bundle Identifier: %s\n URL: %s\n\n UUID: agent\n Identifier: 8.%s.daemon\n Parent Identifier: 2.%s\n Disposition: [%s, allowed, notified]\n", uid, id, id, app, id, id, disposition)
			before := readAppBackgroundItems
			launch := runAppLaunchctl
			t.Cleanup(func() { readAppBackgroundItems = before; runAppLaunchctl = launch })
			readAppBackgroundItems = func() ([]byte, error) { return []byte(body), nil }
			runAppLaunchctl = func(args ...string) ([]byte, error) {
				if len(args) != 2 || args[0] != "print" {
					t.Fatal("doctor mutated supervisor:", args)
				}
				return []byte("Could not find service"), errors.New("not found")
			}
			t.Setenv("DIBS_NO_UPDATE_CHECK", "1")
			t.Setenv("CODEX_HOME", filepath.Join(f.home, ".codex"))
			out, _ := captureStdout(t, func() error { return doctor([]string{"--json"}) })
			var report doctorReport
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatal(err, out)
			}
			found := false
			for _, check := range report.Checks {
				if strings.Contains(check.What, "orphaned background-item registration") {
					found = true
					if !strings.Contains(check.Fix, "configure --service --remove") || !strings.Contains(check.Fix, "brew uninstall --cask dibs") || !strings.Contains(check.Fix, backgroundSettings) {
						t.Fatal("orphan cleanup missing:", check)
					}
				}
			}
			if found != tc.want {
				t.Fatalf("orphan diagnosis=%t want=%t: %s", found, tc.want, out)
			}
			if f.requests.Load() != 0 {
				t.Fatal("orphan diagnosis requested permission")
			}
		})
	}
}

func TestDoctorReportsAppServiceApprovalWithoutRegisteringIt(t *testing.T) {
	f := newAppServiceTestFixture(t)
	if err := paths.WriteServiceDirectory(f.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.state, []byte("requires-approval"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIBS_NO_UPDATE_CHECK", "1")
	t.Setenv("CODEX_HOME", filepath.Join(f.home, ".codex"))
	out, _ := captureStdout(t, func() error { return doctor([]string{"--json"}) })
	var report doctorReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("actual doctor JSON: %v %s", err, out)
	}
	found := false
	for _, check := range report.Checks {
		if check.What == "Dibs background service requires approval" && strings.Contains(check.Fix, "Login Items & Extensions") {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor hid background approval: %s", out)
	}
	body, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"mode":"register"`) || strings.Contains(string(body), `"mode":"unregister"`) || f.requests.Load() != 0 {
		t.Fatalf("read-only doctor changed service or requested access: %s", body)
	}
}

func TestLegacyAppMigrationPreservesPathAndRecoversRefusal(t *testing.T) {
	for _, outcome := range []string{"enabled", "requires-approval", "fail"} {
		t.Run(outcome, func(t *testing.T) {
			f := newAppServiceTestFixture(t)
			legacy := f.legacy(t, "")
			t.Setenv("DIBS_TEST_APP_SERVICE_RESULT", outcome)
			a, err := prepareAppService(f.root, f.dir)
			if err != nil {
				t.Fatal("setup planning:", err)
			}
			var events []string
			loaded := true
			previous := runAppLaunchctl
			t.Cleanup(func() { runAppLaunchctl = previous })
			runAppLaunchctl = func(args ...string) ([]byte, error) {
				events = append(events, args[0])
				switch args[0] {
				case "bootout":
					loaded = false
				case "print":
					if !loaded {
						return []byte("Could not find service"), errors.New("not found")
					}
				case "bootstrap":
					loaded = true
				case "kickstart":
				}
				return nil, nil
			}
			confirmed := 0
			p := &plan{app: a, dir: f.dir, serving: true, stop: func(string) error { events = append(events, "stop"); return nil }, confirm: func(string) error { confirmed++; events = append(events, "confirm"); return nil }}
			err = p.cutover()
			if outcome == "enabled" {
				if err != nil {
					t.Fatal(err)
				}
				pin, found, err := paths.PinnedService()
				if err != nil || !found || !pin.ServicePathSet || pin.ServicePath != "" || pin.ServicePathSource != "migrated" {
					t.Fatalf("literal empty legacy PATH lost: %+v %v", pin, err)
				}
				if _, err := os.Stat(legacy); !os.IsNotExist(err) {
					t.Fatalf("legacy unit survived success: %v", err)
				}
				if loaded || confirmed != 1 || f.requests.Load() != 1 {
					t.Fatalf("duplicate supervisor or missing proof/acquisition: loaded=%t confirmations=%d requests=%d events=%v", loaded, confirmed, f.requests.Load(), events)
				}
			} else {
				if err == nil {
					t.Fatal("registration refusal reported success")
				}
				if !loaded || confirmed != 1 {
					t.Fatalf("legacy service was not recovered: loaded=%t confirmed=%d events=%v", loaded, confirmed, events)
				}
				if _, found, err := paths.PinnedService(); err != nil || found {
					t.Fatalf("failed migration left a new pin: found=%t %v", found, err)
				}
				if f.requests.Load() != 0 {
					t.Fatal("failed migration requested permission")
				}
			}
			if len(events) < 3 || events[0] != "stop" || events[1] != "bootout" || events[2] != "print" {
				t.Fatalf("cutover order=%v", events)
			}
		})
	}
}

func TestLegacyAppMigrationRefusesUnsupportedSettingsBeforeStop(t *testing.T) {
	for _, mutation := range []struct{ name, old, new, want string }{
		{"flag", "<string>-dir</string>", "<string>-allow-parallel</string>", "ProgramArguments"},
		{"environment", "<key>PATH</key>", "<key>DIBS_ADDR</key>", "environment DIBS_ADDR"},
		{"policy", "<string>Interactive</string>", "<string>Adaptive</string>", "ProcessType"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			f := newAppServiceTestFixture(t)
			legacy := f.legacy(t, "/literal path:/bin")
			body, err := os.ReadFile(legacy)
			if err != nil {
				t.Fatal(err)
			}
			body = []byte(strings.Replace(string(body), mutation.old, mutation.new, 1))
			if err := os.WriteFile(legacy, body, 0o600); err != nil {
				t.Fatal(err)
			}
			previous := runAppLaunchctl
			t.Cleanup(func() { runAppLaunchctl = previous })
			runAppLaunchctl = func(...string) ([]byte, error) { t.Fatal("refused migration reached supervisor"); return nil, nil }
			if err := serviceCommand(nil); err == nil || !strings.Contains(err.Error(), mutation.want) {
				t.Fatalf("wrong refusal: %v", err)
			}
			after, err := os.ReadFile(legacy)
			if err != nil || string(after) != string(body) {
				t.Fatal("refusal changed legacy unit")
			}
			if _, found, err := paths.PinnedService(); err != nil || found {
				t.Fatalf("refusal created pin: %t %v", found, err)
			}
		})
	}
}
