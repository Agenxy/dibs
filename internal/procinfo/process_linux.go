// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package procinfo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Native reports that this OS has an in-process identity/inventory backend.
const Native = true

func procPath(pid int, leaf string) string { return filepath.Join("/proc", strconv.Itoa(pid), leaf) }

func bootTime() (int64, error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			boot, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err == nil && boot > 0 {
				return boot, nil
			}
		}
	}
	return 0, fmt.Errorf("/proc/stat has no valid boot time")
}

func processAt(pid int, boot int64) (Process, error) {
	if pid <= 0 {
		return Process{}, os.ErrNotExist
	}
	raw, err := os.ReadFile(procPath(pid, "stat"))
	if err != nil {
		return Process{}, err
	}
	line := string(raw)
	open, end := strings.IndexByte(line, '('), strings.LastIndexByte(line, ')')
	if open < 0 || end <= open {
		return Process{}, fmt.Errorf("invalid process stat comm")
	}
	fields := strings.Fields(line[end+1:])
	if len(fields) < 20 {
		return Process{}, fmt.Errorf("short process stat")
	}
	parent, e1 := strconv.Atoi(fields[1])
	ticks, e2 := strconv.ParseInt(fields[19], 10, 64)
	if e1 != nil || e2 != nil || parent < 0 || ticks < 0 {
		return Process{}, fmt.Errorf("invalid process stat identity")
	}
	// starttime is USER_HZ=100 on Linux, independent of the kernel tick rate.
	return Process{
		PID: pid, PPID: parent, Start: time.Unix(boot+ticks/100, (ticks%100)*int64(time.Second/100)),
		Comm: line[open+1 : end], startTicks: ticks, bootSeconds: boot,
	}, nil
}

// SameProcess rechecks the boot-relative token without re-reading /proc/stat
// for every row. The pass already read that file exactly once in Snapshot.
func SameProcess(row Process) (bool, error) {
	current, err := processAt(row.PID, row.bootSeconds)
	if Gone(err) {
		return false, nil
	}
	return current.startTicks == row.startTicks && current.Start.Equal(row.Start), err
}

// Snapshot reads a fresh /proc table and its shared boot-time basis.
func Snapshot(ctx context.Context) ([]Process, error) {
	boot, err := bootTime()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var rows []Process
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if row, err := processAt(pid, boot); err == nil {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// Start reads the kernel's boot-relative process incarnation.
func Start(pid int) (time.Time, error) {
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, err
	}
	row, err := processAt(pid, boot)
	return row.Start, err
}

// Path reads the executable link, retaining permission errors as unknown.
func Path(pid int) (string, error) { return os.Readlink(procPath(pid, "exe")) }

// Arguments reads argv and inherited environment entries separately.
func Arguments(pid int) ([]string, []string, error) {
	args, err := os.ReadFile(procPath(pid, "cmdline"))
	if err != nil {
		return nil, nil, err
	}
	env, err := os.ReadFile(procPath(pid, "environ"))
	if err != nil {
		return nil, nil, err
	}
	return nullFields(args), nullFields(env), nil
}

// Command reads argv without depending on access to the environment file.
func Command(pid int) ([]string, error) {
	raw, err := os.ReadFile(procPath(pid, "cmdline"))
	return nullFields(raw), err
}

// Environ reads inherited entries without logging their content.
func Environ(pid int) ([]string, error) {
	raw, err := os.ReadFile(procPath(pid, "environ"))
	return nullFields(raw), err
}

func nullFields(raw []byte) []string {
	raw = bytes.TrimSuffix(raw, []byte{0})
	if len(raw) == 0 {
		return nil
	}
	return strings.Split(string(raw), "\x00")
}

// CPU is unused on Linux: liveness already reads USER_HZ totals from /proc.
func CPU(int) (time.Duration, error) { return 0, ErrUnsupported }

// OpenFiles reads descriptor links without opening their target files.
func OpenFiles(pid, maxFD int) ([]string, error) {
	dir := procPath(pid, "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd < 0 || fd > maxFD {
			continue
		}
		if path, err := os.Readlink(filepath.Join(dir, entry.Name())); err == nil {
			files = append(files, path)
		}
	}
	return files, nil
}
