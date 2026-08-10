package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/agenxy/lanes/internal/paths"
)

// writeServiceUnit writes an init-system unit so the daemon survives a closed
// terminal and a reboot.
//
// Every install path told people to run `lanesd &`, which ties the fleet's board
// to one shell. Closing that window, or rebooting, takes the board away from
// every agent still running — and nothing tells them why, because from their
// side coordination simply stops answering. A coordination service that a
// laptop lid can end is not a service.
//
// It WRITES the unit and prints the command to load it, rather than loading it
// itself. Registering a background job that starts at login is a change to the
// machine, not to Lanes, and the operator should be the one who makes it — the
// same reason nothing here ever drives a harness.
func writeServiceUnit() error {
	daemon, err := daemonPath()
	if err != nil {
		return err
	}
	dir := paths.DataDir()

	switch runtime.GOOS {
	case "darwin":
		return writeLaunchAgent(daemon, dir)
	case "linux":
		return writeSystemdUnit(daemon, dir)
	default:
		return fmt.Errorf("no service unit for %s — run `%s` under whatever supervises "+
			"long-lived processes on this system", runtime.GOOS, daemon)
	}
}

// daemonPath resolves lanesd beside this binary first, then PATH.
//
// Beside-first because that is how they are installed together, and a unit file
// records an absolute path that outlives the shell that wrote it: resolving to
// a different lanesd than the one the operator just installed produces a service
// that runs the wrong build indefinitely, with a version skew nothing reports.
func daemonPath() (string, error) {
	if self, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(self), "lanesd")
		if st, err := os.Stat(beside); err == nil && !st.IsDir() {
			return beside, nil
		}
	}
	found, err := exec.LookPath("lanesd")
	if err != nil {
		return "", fmt.Errorf("cannot find lanesd beside %s or on PATH — install it first "+
			"(`task install`, or the release archive)", "lanes")
	}
	return found, nil
}

func writeLaunchAgent(daemon, dir string) error {
	target := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", "dev.agenxy.lanes.plist")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.agenxy.lanes</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + daemon + `</string>
    <string>-dir</string>
    <string>` + dir + `</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>` + filepath.Join(dir, "lanesd.log") + `</string>
  <key>StandardErrorPath</key><string>` + filepath.Join(dir, "lanesd.log") + `</string>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`
	if err := writeUnit(target, plist); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n\nLoad it:\n  launchctl load -w %s\n\n"+
		"KeepAlive restarts it on a crash but not after a clean `lanes stop`, so\n"+
		"stopping it stays stopped until you start it again.\nLogs: %s\n",
		target, target, filepath.Join(dir, "lanesd.log"))
	return nil
}

func writeSystemdUnit(daemon, dir string) error {
	target := filepath.Join(configHome(), "systemd", "user", "lanes.service")
	unit := `[Unit]
Description=Lanes — coordination board for AI agents
Documentation=https://github.com/agenxy/lanes
After=network.target

[Service]
Type=simple
ExecStart=` + daemon + ` -dir ` + dir + `
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`
	if err := writeUnit(target, unit); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n\nEnable and start it:\n  systemctl --user enable --now lanes\n\n"+
		"Logs: journalctl --user -u lanes -f\n", target)
	return nil
}

// configHome honours XDG_CONFIG_HOME, because a systemd user unit written to
// the wrong place is silently never loaded.
func configHome() string {
	if x := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); x != "" {
		return x
	}
	return filepath.Join(os.Getenv("HOME"), ".config")
}

// writeUnit refuses to clobber. An operator who hand-tuned a unit — a different
// port, an environment variable, a resource limit — should not lose it to a
// command that reads as idempotent.
func writeUnit(target, body string) error {
	// target is built in this file from the process's own HOME (or
	// XDG_CONFIG_HOME) plus a fixed filename. No caller-supplied path reaches
	// it, so the taint analysis has nothing to protect against here.
	parent := filepath.Dir(target) // #nosec G703
	// 0o750: the init system reads these as the same user, so nothing wider is
	// needed. ~/Library/LaunchAgents and ~/.config are the user's own.
	if err := os.MkdirAll(parent, 0o750); err != nil { // #nosec G703
		return err
	}
	if _, err := os.Stat(target); err == nil { // #nosec G703
		return fmt.Errorf("%s already exists — delete it first if you want a fresh one, "+
			"or edit it in place; refusing to overwrite a unit you may have tuned", target)
	}
	// #nosec G306,G703 -- a unit file the init system must be able to read, at a
	// path built from HOME and a fixed filename.
	return os.WriteFile(target, []byte(body), 0o644)
}
