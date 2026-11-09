// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package procinfo

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procinfo/libproc"

	"golang.org/x/sys/unix"
)

func TestNativeKernelLayoutAndTimebaseThroughRealCalls(t *testing.T) {
	pid := os.Getpid()
	started, err := Start(pid)
	if err != nil || started.IsZero() || started.After(time.Now()) {
		t.Fatalf("setup: own start unavailable: %v %v", started, err)
	}
	rows, err := Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.PID == pid {
			found = row.Start.Equal(started) && row.PPID == os.Getppid()
		}
	}
	if !found {
		t.Fatal("own PID/start/parent missing from fresh kernel table")
	}
	path, err := Path(pid)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	gotFile, e1 := os.Stat(path)
	wantFile, e2 := os.Stat(want)
	if e1 != nil || e2 != nil || !os.SameFile(gotFile, wantFile) {
		t.Fatalf("kernel path does not name own executable: %q %q", path, want)
	}
	before, err := CPU(pid)
	if err != nil {
		t.Fatal(err)
	}
	var usageBefore, usageAfter time.Duration
	usageBefore = before
	// A real >=50ms CPU increment is necessary: a ns/tick unit mutation still
	// returns positive, increasing numbers and passes a mere monotonicity test.
	var used unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &used); err != nil {
		t.Fatal(err)
	}
	baseline := used.Utime.Nano() + used.Stime.Nano()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for range 10000 {
			runtime.KeepAlive(pid)
		}
		if err := unix.Getrusage(unix.RUSAGE_SELF, &used); err != nil {
			t.Fatal(err)
		}
		if used.Utime.Nano()+used.Stime.Nano()-baseline >= int64(100*time.Millisecond) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("setup: busy fixture did not acquire 100ms of independent getrusage CPU")
		}
	}
	usageAfter, err = CPU(pid)
	if err != nil {
		t.Fatal(err)
	}
	if delta := usageAfter - usageBefore; delta < 50*time.Millisecond || delta > 2*time.Second {
		t.Fatalf("CPU timebase disagrees with busy process: %v", delta)
	}
	f, err := os.CreateTemp(t.TempDir(), "held-transcript-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	files, err := OpenFiles(pid, 65535)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(files, resolved) {
		t.Fatalf("native descriptor layout lost held file %q", resolved)
	}
	args, _, err := Arguments(pid)
	if err != nil || len(args) == 0 {
		t.Fatalf("own argv unavailable: %d %v", len(args), err)
	}
	if _, err := Start(-1); err == nil {
		t.Fatal("invalid PID acquired start identity")
	}
	if _, err := CPU(-1); err == nil {
		t.Fatal("invalid PID acquired CPU evidence")
	}
}

func TestNativeShortStructuresAreErrorsNotMeasuredZeros(t *testing.T) {
	old := readInfo
	t.Cleanup(func() { readInfo = old })
	readInfo = func(call, pid, flavor, arg int, buf []byte) (int, error) { return len(buf) - 1, nil }
	if _, err := CPU(os.Getpid()); err == nil {
		t.Fatal("short task info became measured CPU")
	}
	if _, err := OpenFiles(os.Getpid(), 999); err == nil {
		t.Fatal("partial descriptor record became no open files")
	}
	readInfo = func(call, pid, flavor, arg int, buf []byte) (int, error) {
		if call == pidInfoCall && flavor == listFDs {
			binary.LittleEndian.PutUint32(buf, 3)
			binary.LittleEndian.PutUint32(buf[4:], 1)
			return 8, nil
		}
		return len(buf) - 1, nil
	}
	if _, err := OpenFiles(os.Getpid(), 999); err == nil {
		t.Fatal("short vnode path became no open files")
	}
	readInfo = func(int, int, int, int, []byte) (int, error) { return 0, errors.New("unsupported syscall ABI") }
	if _, err := CPU(os.Getpid()); err == nil {
		t.Fatal("failed syscall became measured CPU")
	}
}

func TestCPUTicksRetainFiftyMillisecondsWithoutOverflow(t *testing.T) {
	if got, err := tickDuration(1200000, 24000000); err != nil || got != 50*time.Millisecond {
		t.Fatalf("24MHz ticks=%v %v", got, err)
	}
	if got, err := tickDuration(50000000, 1000000000); err != nil || got != 50*time.Millisecond {
		t.Fatalf("1GHz ticks=%v %v", got, err)
	}
	if _, err := tickDuration(1, 0); err == nil {
		t.Fatal("zero timebase became CPU evidence")
	}
	if _, err := tickDuration(^uint64(0), 1); err == nil {
		t.Fatal("overflow became CPU evidence")
	}
}

func TestProcargsPreservesArgumentAndEnvironmentBoundaries(t *testing.T) {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, 3)
	raw = append(raw, []byte("/bin/tool\x00\x00/bin/tool\x00\x00argument with spaces\x00DIBS_PARENT=owner\x00PATH=a b\x00\x00")...)
	args, env, err := parseArguments(raw)
	if err != nil || !slices.Equal(args, []string{"/bin/tool", "", "argument with spaces"}) || !slices.Equal(env, []string{"DIBS_PARENT=owner", "PATH=a b"}) {
		t.Fatalf("arguments/environment boundaries: %q %q %v", args, env, err)
	}
	for _, bad := range [][]byte{{1}, append([]byte{9, 0, 0, 0}, []byte("/bin/x\x00x\x00")...)} {
		if _, _, err := parseArguments(bad); err == nil {
			t.Fatal("truncated arguments became environment evidence")
		}
	}
}

// Close a real descriptor after the native list but before its native info
// read. A different held descriptor proves this PID is readable in this pass.
func TestDescriptorCloseRaceRetainsHeldFileWithoutFallback(t *testing.T) {
	for _, raceFirst := range []bool{true, false} {
		t.Run(strconv.FormatBool(raceFirst), func(t *testing.T) {
			closed, err := os.CreateTemp(t.TempDir(), "close-race")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = closed.Close() })
			held, err := os.CreateTemp(t.TempDir(), "held")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = held.Close() })
			closedFD, heldFD := int(closed.Fd()), int(held.Fd())
			original := readInfo
			t.Cleanup(func() { readInfo = original })
			listed, raced := false, false
			readInfo = func(call, pid, flavor, arg int, buf []byte) (int, error) {
				if call == pidInfoCall && flavor == listFDs {
					n, err := original(call, pid, flavor, arg, buf)
					if err != nil {
						return n, err
					}
					seen := map[int]bool{}
					for off := 0; off < n; off += 8 {
						seen[int(binary.LittleEndian.Uint32(buf[off:]))] = true
					}
					if !seen[closedFD] || !seen[heldFD] {
						t.Fatal("setup: real list omitted fixture descriptors")
					}
					first, second := closedFD, heldFD
					if !raceFirst {
						first, second = second, first
					}
					binary.LittleEndian.PutUint32(buf, uint32(first))
					binary.LittleEndian.PutUint32(buf[4:], 1)
					binary.LittleEndian.PutUint32(buf[8:], uint32(second))
					binary.LittleEndian.PutUint32(buf[12:], 1)
					listed = true
					return 16, nil
				}
				if call == pidFDCall && arg == closedFD {
					if !listed {
						t.Fatal("setup: info preceded real list")
					}
					if err := closed.Close(); err != nil {
						t.Fatal(err)
					}
					raced = true
				}
				return original(call, pid, flavor, arg, buf)
			}
			files, err := OpenFiles(os.Getpid(), 65535)
			want, resolveErr := filepath.EvalSymlinks(held.Name())
			if resolveErr != nil {
				t.Fatal(resolveErr)
			}
			if !listed || !raced {
				t.Fatal("setup: native close race did not run")
			}
			if err != nil || !slices.Contains(files, want) {
				t.Fatalf("descriptor close race lost readable held file or requested fallback: %q %v", files, err)
			}
		})
	}
}

func TestUnreadableDescriptorPassStaysUnknownAndDeadPIDStaysAbsent(t *testing.T) {
	original := readInfo
	t.Cleanup(func() { readInfo = original })
	calls := 0
	readInfo = func(call, pid, flavor, arg int, buf []byte) (int, error) {
		calls++
		if call == pidInfoCall && flavor == listFDs {
			binary.LittleEndian.PutUint32(buf, 3)
			binary.LittleEndian.PutUint32(buf[4:], 1)
			return 8, nil
		}
		return 0, libproc.ErrUnavailable
	}
	if files, err := OpenFiles(os.Getpid(), 999); err == nil || Gone(err) || files != nil {
		t.Fatalf("unreadable live descriptor pass became measured empty/absent: %q %v", files, err)
	}
	calls = 0
	if _, err := OpenFiles(2147483647, 999); !Gone(err) || calls != 0 {
		t.Fatalf("dead PID was not absent before descriptor reads: %v calls=%d", err, calls)
	}
}
