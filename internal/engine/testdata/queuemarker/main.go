// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// A private queue-command fixture that records execution in its real cwd.
package main

import "os"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		return
	}
	if len(os.Args) != 3 || os.Args[1] != "queue" || os.Args[2] == "" {
		os.Exit(2)
	}
	if err := os.WriteFile("queue", nil, 0o600); err != nil {
		panic(err)
	}
}
