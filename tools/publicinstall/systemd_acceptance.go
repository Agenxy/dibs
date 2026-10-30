// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const serviceUnit = "dibs.service"

type serviceEvidence struct {
	DataDir        string `json:"data_dir"`
	Unit           string `json:"unit"`
	InitialPID     uint64 `json:"initial_pid"`
	RestartedPID   uint64 `json:"restarted_pid"`
	InitialStart   string `json:"initial_start_monotonic"`
	RestartedStart string `json:"restarted_start_monotonic"`
	Node           string `json:"node"`
	SerialBefore   uint64 `json:"serial_before_reinstall"`
	SerialAfter    uint64 `json:"serial_after_restart"`
	SerialFinal    uint64 `json:"serial_after_second_mcp_pair"`
}

type systemdState struct {
	PID         uint64
	Start, Unit string
}
type serviceBoard struct {
	Node   string `json:"node"`
	Serial uint64 `json:"serial"`
}

type serviceRun struct {
	home, dir, addr, bin, secret string
	client                       *http.Client
	before                       systemdState
	board                        serviceBoard
	evidence                     *serviceEvidence
}

func (r *runner) exerciseSystemdService(bin string) (result error) {
	if runtime.GOOS != "linux" {
		return errors.New("systemd acceptance requires the explicitly isolated hosted Linux job")
	}
	home, dir, addr, err := r.prepareSystemdFixture()
	if err != nil {
		return err
	}
	if err := r.prepareUserManager(); err != nil {
		return err
	}
	if err := r.repeatInstalledConfigure(); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, r.finishSystemdProof()) }()
	run := &serviceRun{home: home, dir: dir, addr: addr, bin: bin, client: &http.Client{Timeout: 5 * time.Second}}
	defer run.client.CloseIdleConnections()
	if err := r.beginSystemdService(run); err != nil {
		return err
	}
	return r.restartSystemdService(run)
}

func (r *runner) beginSystemdService(run *serviceRun) error {
	if _, err := r.command("systemd-daemon-reload", r.env, "/usr/bin/systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if _, err := r.command("systemd-enable-start", r.env, "/usr/bin/systemctl",
		"--user", "enable", "--now", serviceUnit); err != nil {
		return err
	}
	r.report.ServiceLaunch = "enable/start issued; readiness not yet qualified"
	secret, before, err := r.awaitSystemd(run.client, run.addr, run.dir, run.bin)
	if err != nil {
		return err
	}
	evidence := &serviceEvidence{DataDir: run.dir, Unit: before.Unit, InitialPID: before.PID, InitialStart: before.Start}
	r.report.Service = evidence
	if err := r.coordinateProtocols(run.client, run.addr, secret, run.home, 0, true); err != nil {
		return err
	}
	board, err := r.serviceBoard(run.client, run.addr, secret)
	if err != nil {
		return err
	}
	if board.Node == "" || board.Serial == 0 {
		return errors.New("first service MCP pair did not leave durable node/serial evidence")
	}
	evidence.Node, evidence.SerialBefore = board.Node, board.Serial
	run.secret, run.before, run.board, run.evidence = secret, before, board, evidence
	return nil
}

func (r *runner) restartSystemdService(run *serviceRun) error {
	if _, err := r.command("candidate-brew-reinstall", r.env, "brew", "reinstall", "--cask", candidateTap); err != nil {
		return err
	}
	if err := r.repeatInstalledConfigure(); err != nil {
		return err
	}
	if _, err := r.command("systemd-active-after-reinstall", r.env, "/usr/bin/systemctl",
		"--user", "is-active", serviceUnit); err != nil {
		return err
	}
	if _, err := r.command("systemd-reload-after-reinstall", r.env, "/usr/bin/systemctl",
		"--user", "daemon-reload"); err != nil {
		return err
	}
	if _, err := r.command("systemd-restart-after-reinstall", r.env, "/usr/bin/systemctl",
		"--user", "restart", serviceUnit); err != nil {
		return err
	}
	run.client.CloseIdleConnections()
	afterSecret, after, err := r.awaitSystemd(run.client, run.addr, run.dir, run.bin)
	if err != nil {
		return err
	}
	if afterSecret != run.secret || (run.before.PID == after.PID && run.before.Start == after.Start) {
		return errors.New("service restart changed the board credential or provided no fresh process/start evidence")
	}
	run.evidence.RestartedPID, run.evidence.RestartedStart = after.PID, after.Start
	return r.proveServiceContinuity(run)
}

func (r *runner) proveServiceContinuity(run *serviceRun) error {
	afterBoard, err := r.serviceBoard(run.client, run.addr, run.secret)
	if err != nil {
		return err
	}
	if afterBoard.Node != run.board.Node || afterBoard.Serial < run.board.Serial {
		return errors.New("reinstalled service lost the original board node or serial continuity")
	}
	run.evidence.SerialAfter = afterBoard.Serial
	if err := r.coordinateProtocols(run.client, run.addr, run.secret, run.home, 2, true); err != nil {
		return err
	}
	final, err := r.serviceBoard(run.client, run.addr, run.secret)
	if err != nil {
		return err
	}
	if final.Node != run.board.Node || final.Serial <= afterBoard.Serial || r.report.ServiceMCPRoundTrips != 4 {
		return errors.New("second service MCP pair did not advance the same board through the restarted service")
	}
	run.evidence.SerialFinal = final.Serial
	r.report.ServiceLaunch = "systemd user-service start/restart and four service-backed MCP round trips qualified"
	return nil
}

func (r *runner) prepareSystemdFixture() (string, string, string, error) {
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".dibs")
	unit := filepath.Join(home, ".config", "systemd", "user", serviceUnit)
	// #nosec G703 -- fixed user unit in the explicitly ephemeral hosted runner
	if _, err := os.Lstat(unit); !os.IsNotExist(err) {
		return "", "", "", errors.New("fresh candidate cask unexpectedly configured a service before the documented command")
	}
	// #nosec G703 -- fresh isolated hosted-runner board, existing state is refused
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", "", "", err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", "", err
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", "", "", err
	}
	config := fmt.Sprintf("addr = %q\n[wake]\nsockets = false\n", addr)
	// #nosec G703 -- fixed configuration below the just-created private hosted-runner board
	if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(config), 0o600); err != nil {
		return "", "", "", err
	}
	r.env = append(r.env, "DIBS_ADDR="+addr, "SYSTEMD_COLORS=0")
	return home, dir, addr, nil
}

func (r *runner) prepareUserManager() error {
	account, err := user.Current()
	if err != nil {
		return err
	}
	uid := strconv.Itoa(os.Getuid())
	if account.Uid != uid || uid == "0" {
		return errors.New("hosted service acceptance requires the runner's ordinary user account")
	}
	runtimeDir := filepath.Join("/run/user", uid)
	r.env = append(r.env, "XDG_RUNTIME_DIR="+runtimeDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path="+filepath.Join(runtimeDir, "bus"))
	if _, err := r.command("systemd-enable-linger", r.env, "sudo", "-n", "/usr/bin/loginctl",
		"enable-linger", account.Username); err != nil {
		return err
	}
	_, err = r.command("systemd-start-user-manager", r.env, "sudo", "-n", "/usr/bin/systemctl",
		"start", "user@"+uid+".service")
	return err
}

func (r *runner) awaitSystemd(client *http.Client, addr, dir, bin string) (string, systemdState, error) {
	ctx, cancel := context.WithTimeout(r.ctx, time.Minute)
	defer cancel()
	secret, err := awaitDaemon(ctx, client, addr, dir)
	if err != nil {
		return "", systemdState{}, err
	}
	if _, err := r.command("systemd-is-active", r.env, "/usr/bin/systemctl",
		"--user", "is-active", serviceUnit); err != nil {
		return "", systemdState{}, err
	}
	out, err := r.command("systemd-process-evidence", r.env, "/usr/bin/systemctl", "--user", "show", serviceUnit,
		"--property=MainPID", "--property=ActiveState",
		"--property=ExecMainStartTimestampMonotonic", "--property=FragmentPath")
	if err != nil {
		return "", systemdState{}, err
	}
	state, err := parseSystemdState(out)
	if err != nil {
		return "", state, err
	}
	if err := checkServiceProcess(state, dir, bin); err != nil {
		return "", state, err
	}
	return secret, state, nil
}

func parseSystemdState(out string) (systemdState, error) {
	values := make(map[string]string)
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return systemdState{}, errors.New("malformed systemd process evidence")
		}
		if _, duplicate := values[key]; duplicate {
			return systemdState{}, errors.New("duplicate systemd process evidence")
		}
		values[key] = value
	}
	pid, err := strconv.ParseUint(values["MainPID"], 10, 32)
	start, startErr := strconv.ParseUint(values["ExecMainStartTimestampMonotonic"], 10, 64)
	if err != nil || startErr != nil || pid == 0 || start == 0 ||
		values["ActiveState"] != "active" || !filepath.IsAbs(values["FragmentPath"]) {
		return systemdState{}, errors.New("systemd service lacks active PID/start/unit evidence")
	}
	return systemdState{PID: pid, Start: values["ExecMainStartTimestampMonotonic"], Unit: values["FragmentPath"]}, nil
}

func checkServiceProcess(state systemdState, dir, bin string) error {
	wantUnit := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user", serviceUnit)
	if state.Unit != wantUnit {
		return errors.New("systemd loaded another unit instead of the candidate's configured user unit")
	}
	proc := filepath.Join("/proc", strconv.FormatUint(state.PID, 10))
	// #nosec G304 -- kernel proc directory selected by parsed numeric MainPID
	command, err := os.ReadFile(filepath.Join(proc, "cmdline"))
	if err != nil {
		return err
	}
	args := bytes.Split(command, []byte{0})
	matched := false
	for i := 0; i+1 < len(args); i++ {
		if string(args[i]) == "-dir" && string(args[i+1]) == dir {
			matched = true
		}
	}
	if !matched {
		return errors.New("systemd process does not name the selected board directory")
	}
	actual, err := os.Stat(filepath.Join(proc, "exe"))
	if err != nil {
		return err
	}
	installed, err := os.Stat(filepath.Join(bin, "dibd"))
	if err != nil {
		return err
	}
	if !os.SameFile(actual, installed) {
		return errors.New("systemd process is not running the current installed daemon image")
	}
	return nil
}

func (r *runner) serviceBoard(client *http.Client, addr, secret string) (serviceBoard, error) {
	var board serviceBoard
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, "http://"+addr+"/api/board", nil)
	if err != nil {
		return board, err
	}
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := client.Do(req)
	if err != nil {
		return board, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return board, fmt.Errorf("service board continuity query returned HTTP %d", resp.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&board)
	return board, err
}

func (r *runner) finishSystemdProof() error {
	_, statusErr := r.command("systemd-final-status", r.env, "/usr/bin/systemctl",
		"--user", "status", "--no-pager", "--full", serviceUnit)
	_, journalErr := r.command("systemd-journal-tail", r.env, "sudo", "-n", "/usr/bin/journalctl",
		"--no-pager", "--lines=80",
		"_SYSTEMD_USER_UNIT="+serviceUnit, "_UID="+strconv.Itoa(os.Getuid()))
	_, cleanupErr := r.command("systemd-fixture-disable-stop", r.env, "/usr/bin/systemctl",
		"--user", "disable", "--now", serviceUnit)
	return errors.Join(statusErr, journalErr, cleanupErr)
}
