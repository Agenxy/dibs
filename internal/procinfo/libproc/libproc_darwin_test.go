// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package libproc

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLibraryABI(t *testing.T) {
	pid := os.Getpid()
	var task [96]byte
	readCPU := func() uint64 {
		t.Helper()
		n, err := PIDInfo(pid, 4, 0, task[:])
		if err != nil || n != len(task) {
			t.Fatalf("PIDTASKINFO layout: n=%d err=%v", n, err)
		}
		return binary.LittleEndian.Uint64(task[16:24]) + binary.LittleEndian.Uint64(task[24:32])
	}
	freq, err := unix.SysctlUint64("hw.tbfrequency")
	if err != nil || freq == 0 {
		t.Fatal("setup timebase:", err)
	}
	before := readCPU()
	var used unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &used); err != nil {
		t.Fatal(err)
	}
	initial := used.Utime.Nano() + used.Stime.Nano()
	start := time.Now()
	for {
		for range 10000 {
			runtime.KeepAlive(pid)
		}
		if err := unix.Getrusage(unix.RUSAGE_SELF, &used); err != nil {
			t.Fatal(err)
		}
		if used.Utime.Nano()+used.Stime.Nano()-initial >= int64(100*time.Millisecond) {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatal("setup: independent CPU fixture did not run")
		}
	}
	ticks := readCPU() - before
	cpu := time.Duration(ticks * uint64(time.Second) / freq)
	if cpu < 50*time.Millisecond || cpu > 2*time.Second {
		t.Fatalf("libSystem CPU/timebase mismatch: ticks=%d hz=%d cpu=%v", ticks, freq, cpu)
	}
	f, err := os.CreateTemp(t.TempDir(), "held")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	var list [16384]byte
	n, err := PIDInfo(pid, 1, 0, list[:])
	if err != nil || n%8 != 0 {
		t.Fatalf("PIDLISTFDS: n=%d err=%v", n, err)
	}
	found := false
	for off := 0; off < n; off += 8 {
		fd, kind := binary.LittleEndian.Uint32(list[off:]), binary.LittleEndian.Uint32(list[off+4:])
		if kind != 1 || uintptr(fd) != f.Fd() {
			continue
		}
		var vnode [1200]byte
		size, err := PIDFDInfo(pid, int(fd), 2, vnode[:])
		if err != nil || size != len(vnode) {
			t.Fatalf("VNODEPATHINFO: n=%d err=%v", size, err)
		}
		end := bytes.IndexByte(vnode[176:], 0)
		if end <= 0 {
			t.Fatal("empty native vnode path")
		}
		got := string(vnode[176 : 176+end])
		want, err := filepath.EvalSymlinks(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("held native file differs: %q want%q", got, want)
		}
		found = true
	}
	if !found {
		t.Fatal("native FD inventory omitted actual held file")
	}
	var path [4096]byte
	length, err := PIDPath(pid, path[:])
	if err != nil || length <= 0 {
		t.Fatalf("PIDPATH: n=%d err=%v", length, err)
	}
	end := bytes.IndexByte(path[:], 0)
	if end != length {
		t.Fatalf("PIDPATH length=%d terminator=%d", length, end)
	}
	t.Logf("arch=%s task=96 vnode=1200 path_offset=176 ticks=%d hz=%d cpu=%v wall=%v", runtime.GOARCH, ticks, freq, cpu, time.Since(start))
	if _, err := PIDInfo(2147483647, 4, 0, task[:]); err == nil {
		t.Fatal("missing PID became zero CPU evidence")
	}
	if _, err := PIDInfo(2147483647, 1, 0, list[:]); err == nil {
		t.Fatal("missing PID became no open files")
	}
}

func TestConcurrentLibraryResultsStayUnknownOnRefusal(t *testing.T) {
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 32 {
				var buf [96]byte
				if _, err := PIDInfo(2147483647, 4, 0, buf[:]); err == nil {
					t.Errorf("missing PID became readable evidence: %v", err)
					return
				}
				if _, err := PIDInfo(os.Getpid(), 99999, 0, buf[:]); err == nil {
					t.Errorf("refused live PID became readable evidence: %v", err)
					return
				}
				if n, err := PIDInfo(os.Getpid(), 4, 0, buf[:]); err != nil || n != len(buf) {
					t.Errorf("successful call lost its layout: n=%d err=%v", n, err)
					return
				}
			}
		})
	}
	workers.Wait()
}

func TestAmbiguousZeroResultStaysUnknown(t *testing.T) {
	var buf [96]byte
	n, err := withBuffer(os.Getpid(), buf[:], func(uintptr) uintptr { return 0 })
	if n != 0 || err == nil {
		t.Fatalf("ambiguous zero became measured information: n=%d err=%v", n, err)
	}
}
