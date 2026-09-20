package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/agenxy/dibs/internal/paths"
)

// hostBridgeUnit is `dibs host-bridge --service`: an init-system unit that
// keeps this machine's bridge to a board elsewhere running, so its agents
// stay reachable after the terminal that started the bridge is closed and
// after a reboot. The same reasoning as `dibs configure --service`, for the
// same reason: a route that a laptop lid can end is not a route.
//
// It writes the unit and prints the command to load it, rather than loading
// it itself, exactly as the daemon's unit is handled: registering a job that
// starts at login is a change to the machine, and the operator makes it.
//
// The unit carries the two variables the join recipe printed, DIBS_ADDR and
// DIBS_DIR, and the two that may accompany them, DIBS_BOARD_PEER (the hub as
// a Supgang peer, re-resolved on every start) and DIBS_HOST_ID (an identity
// stated outright). Nothing else: the secret stays in the data directory,
// and the argv is `dibs host-bridge` and no more.
func hostBridgeUnit() error {
	if err := checkConfigReadable(); err != nil {
		return err
	}
	if _, err := localSecret(); err != nil {
		return fmt.Errorf("no local secret in %s: copy the board's secret there first, as the "+
			"recipe from `dibs mcp-config --board` says: %w", paths.DataDir(), err)
	}
	dir, err := filepath.Abs(paths.DataDir())
	if err != nil {
		return fmt.Errorf("resolving the data directory to an absolute path: %w", err)
	}
	// A unit for a bridge that can start nobody would run forever and cover
	// nothing: the same refusal the command itself makes.
	if _, err := localWakeRoutes(dir); err != nil {
		return err
	}
	addr := strings.TrimSpace(os.Getenv("DIBS_ADDR"))
	if addr == "" {
		return fmt.Errorf("DIBS_ADDR is not set: the unit must name the board this bridge joins, " +
			"and that is the address `dibs mcp-config --board` printed beside DIBS_DIR")
	}
	env := map[string]string{"DIBS_ADDR": addr, "DIBS_DIR": dir}
	for _, k := range []string{boardPeerEnv, "DIBS_HOST_ID"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			env[k] = v
		}
	}
	env = unitEnv(env)
	bin := self()
	for _, v := range append([]string{bin, dir}, valuesOf(env)...) {
		if err := usableInAUnitFile(v); err != nil {
			return err
		}
	}
	target, body, load, err := bridgeUnit(runtimeGOOS(), bin, dir, env)
	if err != nil {
		return err
	}
	if err := writeUnit(target, body); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n\n%s", target, load)
	return nil
}

// runtimeGOOS is the platform the unit is written for; a variable so a test
// can ask which target hostBridgeUnit chose.
func runtimeGOOS() string { return runtime.GOOS }

func valuesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// bridgeUnitSlug names the unit after the data directory, which the join
// recipe already named after the board: one unit per board this machine has
// joined, and a name an operator can read in `launchctl list`. The readable
// part is lossy (`hub:4777` and `hub-4777` read the same, and two
// directories can share a basename), so a digest of the whole absolute path
// follows it: two directories are two units, whatever they are called.
// unitEnv adds the installing shell's PATH to a unit's environment.
//
// A supervisor starts a job with a PATH of its own (`/usr/bin:/bin:/usr/sbin:
// /sbin` under launchd, near enough under systemd --user), which has neither
// ~/.local/bin nor Homebrew in it, which is where `claude` and `codex` live.
// A wake entry written the way docs/CONFIGURATION.md writes it then failed
// under the unit with "executable file not found" while it worked from the
// terminal that installed the unit, and doctor counted the route as covering
// because the entry was there. The operator's PATH at install time is the
// PATH their terminal resolved the command on, so the unit gets that one.
// Found by the pre-release review, round four.
func unitEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env)+1)
	for k, v := range env {
		out[k] = v
	}
	if p := strings.TrimSpace(os.Getenv("PATH")); p != "" {
		out["PATH"] = p
	}
	return out
}

func bridgeUnitSlug(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return readableSlug(filepath.Base(dir)) + "-" + hex.EncodeToString(sum[:4])
}

func readableSlug(name string) string {
	base := strings.TrimPrefix(name, ".")
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "board"
	}
	return b.String()
}

// bridgeUnit is the unit for one platform: where it goes, what it says, and
// how to load it. Split from the writing so both platforms can be checked
// on one.
func bridgeUnit(goos, bin, dir string, env map[string]string) (target, body, load string, err error) {
	slug := bridgeUnitSlug(dir)
	logPath := filepath.Join(dir, "host-bridge.log")
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	switch goos {
	case "darwin":
		label := "org.agenxy.dibs-bridge-" + slug
		target = filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", label+".plist")
		var vars strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&vars, "    <key>%s</key><string>%s</string>\n", k, xmlText(env[k]))
		}
		body = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + label + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + xmlText(bin) + `</string>
    <string>host-bridge</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
` + vars.String() + `  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>` + xmlText(logPath) + `</string>
  <key>StandardErrorPath</key><string>` + xmlText(logPath) + `</string>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`
		load = "Load it:\n  launchctl load -w " + shellArg(target) + "\n\n" +
			"KeepAlive restarts it on a crash and after the hub refused the stream, with\n" +
			"launchd's own pause between tries; `launchctl unload -w` stops it for good.\n" +
			"Logs: " + logPath + "\n"
	case "linux":
		name := "dibs-bridge-" + slug
		target = filepath.Join(configHome(), "systemd", "user", name+".service")
		var vars strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&vars, "Environment=%s\n", systemdArg(k+"="+env[k]))
		}
		body = `[Unit]
Description=Dibs host bridge: this machine's wake commands for a board served elsewhere
Documentation=https://github.com/agenxy/dibs
After=network.target

[Service]
Type=simple
` + vars.String() + `ExecStart=` + systemdArg(bin) + ` host-bridge
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
		load = "Enable and start it:\n  systemctl --user enable --now " + name + "\n\n" +
			"Logs: journalctl --user -u " + name + " -f\n"
	default:
		return "", "", "", fmt.Errorf("no unit format for %s: run `dibs host-bridge` under the supervisor "+
			"this machine has, with DIBS_ADDR and DIBS_DIR in its environment", goos)
	}
	return target, body, load, nil
}
