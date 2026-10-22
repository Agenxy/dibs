// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin

package paths

import (
	"bytes"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// spellingBudget bounds how long a caller waits for the native spelling. An
// ordinary open answers in microseconds.
const spellingBudget = 250 * time.Millisecond

// openForSpelling is the open the probe makes, replaced by tests.
var openForSpelling = func(p string) (*os.File, error) {
	// #nosec G304 -- local canonicalization; event-only cannot block on a FIFO
	return os.OpenFile(p, syscall.O_EVTONLY, 0)
}

type spellingProbe struct {
	done   chan struct{}
	result string
}

var spellingProbes = struct {
	sync.Mutex
	running map[string]*spellingProbe
}{running: map[string]*spellingProbe{}}

// nativeSpelling never waits on the filesystem past spellingBudget.
//
// open(2) can block in the kernel with no deadline. On 2026-10-22 the
// launchd-run daemon's opens of ~/Desktop paths stopped returning (macOS
// privacy control for the Desktop folder, by every sign), and because this
// ran inline on hook_poll and declare, 375 requests piled up behind it and
// declare hung for every agent working under ~/Desktop. The spelling is
// evidence, not authority, and failure already keeps the name given, so a
// slow answer is a failed one. One probe per path runs at a time: a probe
// that never returns costs one goroutine, not one per request.
func nativeSpelling(p string) string {
	spellingProbes.Lock()
	probe, running := spellingProbes.running[p]
	if running {
		spellingProbes.Unlock()
		select {
		case <-probe.done:
			return probe.result
		default:
			return p // an earlier probe of this path is still waiting
		}
	}
	probe = &spellingProbe{done: make(chan struct{})}
	spellingProbes.running[p] = probe
	spellingProbes.Unlock()
	go func() {
		probe.result = probeSpelling(p)
		spellingProbes.Lock()
		delete(spellingProbes.running, p)
		spellingProbes.Unlock()
		close(probe.done)
	}()
	timer := time.NewTimer(spellingBudget)
	defer timer.Stop()
	select {
	case <-probe.done:
		return probe.result
	case <-timer.C:
		reportSpellingTimeout(p)
		return p
	}
}

// EvalSymlinks does not correct case on an insensitive filesystem. Ask the
// opened object for its native path, then check it still names that object.
// Failure keeps today's spelling; this is filesystem evidence, not authority.
func probeSpelling(p string) string {
	f, err := openForSpelling(p)
	if err != nil {
		return p
	}
	defer func() { _ = f.Close() }()
	var buf [1024]byte // Darwin MAXPATHLEN, required by F_GETPATH
	// #nosec G103 -- fixed F_GETPATH command writes only MAXPATHLEN into this live, fixed-size buffer
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return p
	}
	end := bytes.IndexByte(buf[:], 0)
	if end <= 0 {
		return p
	}
	native := string(buf[:end])
	opened, err := f.Stat()
	if err != nil {
		return p
	}
	current, err := os.Stat(native)
	if err != nil || !os.SameFile(opened, current) {
		return p
	}
	return native
}
