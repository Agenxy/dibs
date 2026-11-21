// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package procinfo

import (
	"io"
	"os"
	"testing"

	"github.com/agenxy/dibs/internal/procexec"
)

func TestOwnsStdioPipePeers(t *testing.T) {
	child := procexec.Command(t.Context(), os.Args[0], "-test.run=^TestAntigravityPipeChild$")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = child.Wait()
	})
	var ready [1]byte
	if _, err := io.ReadFull(output, ready[:]); err != nil || ready[0] != 'y' {
		t.Fatal("pipe child did not become ready", err)
	}
	if !OwnsStdioPipes(child.Process.Pid, os.Getpid()) {
		t.Fatal("parent's two live pipe peers were missed")
	}
	if OwnsStdioPipes(child.Process.Pid, child.Process.Pid) {
		t.Fatal("pipe endpoints passed as their own peers")
	}
}

func TestAntigravityPipeChild(t *testing.T) {
	if len(os.Args) != 2 || os.Args[1] != "-test.run=^TestAntigravityPipeChild$" {
		return
	}
	if _, err := os.Stdout.WriteString("y"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}
