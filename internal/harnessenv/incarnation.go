// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// AppIncarnation is a process observation, never an agent or session binding.
// Start distinguishes PID reuse; the host is carried separately by the caller.
type AppIncarnation struct {
	PID   int
	Start string
}

type processIdentity struct {
	parent int
	start  string
	path   string
}

// ProcessStart supplies the bridge's own incarnation on stateless requests.
func ProcessStart(pid int) string {
	processes, _ := processInventory()
	return processes[pid].start
}

// AppForBridge independently verifies the claimed bridge process and walks
// its actual ancestry. No directory, session name, or model-supplied surface
// can select a row. Probing is bounded and happens once per bridge incarnation.
func AppForBridge(pid int, started string) (AppIncarnation, bool, error) {
	if pid <= 1 || started == "" {
		return AppIncarnation{}, false, nil
	}
	processes, err := processInventory()
	if err != nil {
		return AppIncarnation{}, false, err
	}
	if processes[pid].start != started {
		return AppIncarnation{}, false, nil
	}
	for range 12 {
		p, ok := processes[pid]
		if !ok || p.parent <= 1 || p.parent == pid {
			break
		}
		pid = p.parent
		parent := processes[pid]
		if strings.HasSuffix(parent.path, "/ChatGPT.app/Contents/MacOS/ChatGPT") {
			return AppIncarnation{PID: pid, Start: parent.start}, parent.start != "", nil
		}
		if Classify([]string{parent.path}) == ClaudeDesktop {
			break // a nearer harness owns this bridge
		}
	}
	return AppIncarnation{}, false, nil
}

func processInventory() (map[int]processIdentity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := appProbeOutput(ctx, "/bin/ps", "-axo", "pid=,ppid=,lstart=,comm=")
	if err != nil {
		return nil, err
	}
	if len(raw) > 4<<20 {
		return nil, errors.New("process inventory exceeds bound")
	}
	return parseProcessInventory(string(raw)), nil
}

func parseProcessInventory(raw string) map[int]processIdentity {
	out := map[int]processIdentity{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		parent, parentErr := strconv.Atoi(f[1])
		if err == nil && parentErr == nil && pid > 1 {
			out[pid] = processIdentity{parent: parent, start: strings.Join(f[2:7], " "), path: strings.Join(f[7:], " ")}
		}
	}
	return out
}
