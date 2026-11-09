// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/agenxy/dibs/internal/procinfo"
)

var (
	nativeProcessSnapshot = procinfo.Snapshot
	nativeProcessPath     = procinfo.Path
	nativeProcessSame     = procinfo.SameProcess
	nativeProcessStart    = procinfo.Start
)

type processPresence uint8

const (
	processUnknown processPresence = iota
	processAbsent
	processPresent
)

type processPath struct {
	state processPresence
	value string
	err   error
}

// One table and its lazy path observations live for one decision pass. No
// successful path or error survives into the next pass. Only requested
// ancestors and app/runtime candidates cause executable-path reads.
type processPass struct {
	ctx    context.Context
	rows   map[int]processIdentity
	paths  map[int]processPath
	native bool
}

var appProcessPass = readProcessPass

func readProcessPass(ctx context.Context) (*processPass, error) {
	p := &processPass{ctx: ctx, rows: map[int]processIdentity{}, paths: map[int]processPath{}}
	if !procinfo.Native {
		raw, err := appProbeOutput(ctx, "/bin/ps", "-axo", "pid=,ppid=,lstart=,comm=")
		if err != nil {
			return nil, err
		}
		if len(raw) > 4<<20 {
			return nil, errors.New("process inventory exceeds bound")
		}
		p.rows = parseProcessInventory(string(raw))
		return p, nil
	}
	rows, err := nativeProcessSnapshot(ctx)
	if err != nil {
		return nil, err
	} // unknown table never triggers a whole-table retry
	for _, row := range rows {
		p.rows[row.PID] = processIdentity{parent: row.PPID, start: procinfo.Stamp(row.Start), comm: row.Comm, kernel: row}
	}
	p.native = true
	return p, nil
}

func (p *processPass) present(pid int) (bool, error) {
	if err := p.ctx.Err(); err != nil {
		return false, err
	}
	row, ok := p.rows[pid]
	if !ok {
		if !p.native {
			return false, nil
		}
		_, err := nativeProcessStart(pid)
		if procinfo.Gone(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return false, errors.New("process exists outside the readable inventory")
	}
	if row.kernel.PID == 0 {
		return true, nil
	} // explicit fixture/legacy evidence
	return nativeProcessSame(row.kernel)
}

func (p *processPass) path(pid int) processPath {
	if observed, ok := p.paths[pid]; ok {
		return observed
	}
	observed := p.readPath(pid)
	p.paths[pid] = observed
	return observed
}

func (p *processPass) readPath(pid int) processPath {
	if err := p.ctx.Err(); err != nil {
		return processPath{state: processUnknown, err: err}
	}
	row, ok := p.rows[pid]
	if !ok {
		if _, err := p.present(pid); err != nil {
			return processPath{state: processUnknown, err: err}
		}
		return processPath{state: processAbsent}
	}
	if row.kernel.PID == 0 {
		if row.path == "" {
			return processPath{state: processUnknown, err: errors.New("process path unavailable")}
		}
		return processPath{state: processPresent, value: row.path}
	}
	path, err := nativeProcessPath(pid)
	if procinfo.Gone(err) {
		return processPath{state: processAbsent}
	}
	// Recheck before a fallback as well, so a departed or reused PID never
	// buys a command or splices another incarnation's path onto this row.
	same, checkErr := nativeProcessSame(row.kernel)
	if checkErr != nil {
		return processPath{state: processUnknown, err: errors.Join(err, checkErr)}
	}
	if !same {
		return processPath{state: processAbsent}
	}
	if err != nil {
		procinfo.WarnFallback("process executable path", err)
		path, err = processPathFallback(p.ctx, pid)
		// The command may have raced a second incarnation change.
		same, checkErr = nativeProcessSame(row.kernel)
		if checkErr != nil {
			return processPath{state: processUnknown, err: errors.Join(err, checkErr)}
		}
		if !same {
			return processPath{state: processAbsent}
		}
	}
	if err != nil {
		return processPath{state: processUnknown, err: err}
	}
	if path == "" {
		return processPath{state: processUnknown, err: errors.New("empty executable-path evidence")}
	}
	return processPath{state: processPresent, value: path}
}

var processPathFallback = func(ctx context.Context, pid int) (string, error) {
	out, err := appProbeOutput(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "comm=")
	if err != nil {
		return "", err
	}
	if len(out) > 4096 {
		return "", errors.New("process path exceeds bound")
	}
	return strings.TrimSpace(string(out)), nil
}

// Comm selects candidates, never authority. Only an observed executable path
// can place a process in the app bundle. Include helper names so a helper-only
// remnant remains unknown rather than becoming a known running incarnation.
func appProcessCandidate(row processIdentity) bool {
	if row.kernel.PID == 0 {
		return true
	}
	return row.comm == "codex" || strings.HasPrefix(row.comm, "ChatGPT") || strings.HasPrefix(row.comm, "chrome_crashpad")
}

func (p *processPass) appEvidence() (ThreadOwnership, []string, error) {
	var ids []int
	for pid, row := range p.rows {
		if appProcessCandidate(row) {
			ids = append(ids, pid)
		}
	}
	sort.Ints(ids)
	var pids, epochs []string
	appEpoch := ""
	running := false
	for _, pid := range ids {
		observed := p.path(pid)
		if observed.state == processUnknown {
			return ThreadOwnership{}, nil, fmt.Errorf("app candidate path unknown: %w", observed.err)
		}
		if observed.state == processAbsent || !strings.HasPrefix(observed.value, chatGPTRoot) {
			continue
		}
		running = true
		epoch := strconv.Itoa(pid) + ":" + p.rows[pid].start
		if observed.value == chatGPTRoot+"Contents/MacOS/ChatGPT" {
			appEpoch = epoch
		}
		if filepath.Base(observed.value) == "codex" {
			pids = append(pids, strconv.Itoa(pid))
			epochs = append(epochs, epoch)
		}
	}
	if !running {
		return ThreadOwnership{Known: true, Epoch: "absent"}, nil, nil
	}
	if appEpoch == "" {
		sort.Strings(epochs)
		appEpoch = strings.Join(epochs, ",")
	}
	return ThreadOwnership{Running: true, Epoch: appEpoch}, pids, nil
}
