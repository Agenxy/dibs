// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// A private harness-shaped process with an empty environment and owned stdin.
package main

import (
	"io"
	"os"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		return
	}
	if len(os.Environ()) != 0 {
		panic("fixture environment is not empty")
	}
	if _, err := io.WriteString(os.Stdout, "R"); err != nil {
		panic(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}
