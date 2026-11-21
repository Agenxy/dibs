// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/agenxy/dibs/internal/procexec"
)

func main() {
	if i := slices.Index(os.Args, "--child"); i >= 0 && i+1 < len(os.Args) {
		args := os.Args[i+1:]
		child := procexec.Command(context.Background(), args[0], args[1:]...)
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			panic(err)
		}
		return
	}
	fmt.Printf("%d %t\n", os.Getpid(), os.Getenv("CODEX_THREAD_ID") == "test-shell-thread")
	_, _ = io.Copy(io.Discard, os.Stdin)
}
