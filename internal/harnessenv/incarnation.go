// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package harnessenv

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/procinfo"
)

// AppIncarnation is a process observation, never an agent or session binding.
// Start distinguishes PID reuse; the host is carried separately by the caller.
type AppIncarnation struct {
	PID   int
	Start string
}

// BridgeIncarnation is claimed local process evidence, verified against one
// inventory. Neither this evidence nor its result selects an agent identity.
type BridgeIncarnation struct {
	PID   int
	Start string
}

// BridgeObservation distinguishes a departed incarnation from a live bridge
// outside the app, so only genuinely abandoned pending recovery reports a fault.
type BridgeObservation struct {
	Present bool // the PID and start stamp both still match
	App     AppIncarnation
}

type processIdentity struct {
	parent int
	start  string
	path   string
	comm   string
	kernel procinfo.Process
}

// ProcessStart supplies the bridge's own incarnation on stateless requests.
func ProcessStart(pid int) string {
	if pid == os.Getpid() {
		if started, supported := nativeSelfStart(); supported {
			if started.IsZero() {
				return ""
			}
			// processInventory normalizes ps's padded day with strings.Fields.
			return started.UTC().Format("Mon Jan 2 15:04:05 2006")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	processes, _ := processInventoryContext(ctx)
	if processes == nil {
		return ""
	}
	return processes.rows[pid].start
}

// AppForBridge independently verifies the claimed bridge process and walks
// its actual ancestry. No directory, session name, or model-supplied surface
// can select a row. Probing is bounded and happens once per bridge incarnation.
func AppForBridge(pid int, started string) (AppIncarnation, bool, error) {
	if pid <= 1 || started == "" {
		return AppIncarnation{}, false, nil
	}
	observed, err := AppForBridges(context.Background(), []BridgeIncarnation{{pid, started}})
	if err != nil {
		return AppIncarnation{}, false, err
	}
	app := observed[0].App
	return app, app.PID > 1, nil
}

// AppForBridges takes one bounded inventory for the entire retry batch, not
// one subprocess per pending bridge. Cancellation belongs to the daemon.
func AppForBridges(ctx context.Context, bridges []BridgeIncarnation) ([]BridgeObservation, error) {
	if len(bridges) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	processes, err := processInventoryContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]BridgeObservation, len(bridges))
	for i, bridge := range bridges {
		if bridge.PID <= 1 || bridge.Start == "" {
			continue
		}
		present, err := processes.present(bridge.PID)
		if err != nil {
			return nil, err
		}
		if !present || processes.rows[bridge.PID].start != bridge.Start {
			continue
		}
		out[i].Present = true
		out[i].App, err = appInInventory(processes, bridge.PID, bridge.Start)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func appInInventory(processes *processPass, pid int, started string) (AppIncarnation, error) {
	if processes.rows[pid].start != started {
		return AppIncarnation{}, nil
	}
	for range 12 {
		p, ok := processes.rows[pid]
		if !ok || p.parent <= 1 || p.parent == pid {
			break
		}
		pid = p.parent
		parent := processes.rows[pid]
		path := processes.path(pid)
		if path.state == processUnknown {
			return AppIncarnation{}, path.err
		}
		if path.state == processAbsent {
			break
		}
		if strings.HasSuffix(path.value, "/ChatGPT.app/Contents/MacOS/ChatGPT") {
			if parent.start != "" {
				return AppIncarnation{PID: pid, Start: parent.start}, nil
			}
			break
		}
		if Classify([]string{path.value}) == ClaudeDesktop {
			break // a nearer harness owns this bridge
		}
	}
	return AppIncarnation{}, nil
}

func processInventoryContext(ctx context.Context) (*processPass, error) {
	return appProcessPass(ctx)
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
