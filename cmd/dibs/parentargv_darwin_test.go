// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build darwin

package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseProcargs2ReadsArgvAndStopsBeforeTheEnvironment(t *testing.T) {
	raw := binary.LittleEndian.AppendUint32(nil, 3)
	raw = append(raw, "/usr/local/bin/claude\x00\x00\x00\x00claude\x00--dangerously-load-development-channels\x00plugin:dibs@dibs\x00HOME=/Users/x\x00"...)
	want := []string{"claude", "--dangerously-load-development-channels", "plugin:dibs@dibs"}
	if got := parseProcargs2(raw); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// The real kernel answer for this process's parent, the go tool.
func TestParentArgvReadsTheRealParent(t *testing.T) {
	if argv := parentArgv(); len(argv) == 0 || argv[0] == "" {
		t.Fatalf("no parent arguments read from the kernel: %q", argv)
	}
}

// The kernel's record of a live process's executable, read for this very
// test binary: the same read doctor makes for the running app service.
func TestProcessExecutableNamesALiveProcess(t *testing.T) {
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := processExecutable(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	wantReal, _ := filepath.EvalSymlinks(want)
	gotReal, _ := filepath.EvalSymlinks(got)
	if gotReal == "" || gotReal != wantReal {
		t.Fatalf("processExecutable(self) = %q, want %q", got, want)
	}
}
