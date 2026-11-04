// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Pure external recorder for the upgrade's actual detached restart argv.
package main

import (
	"encoding/json"
	"os"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--prepare" {
		return
	}
	raw, err := json.Marshal(os.Args[1:])
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Getenv("DIBS_TEST_RESTART_ARGV"), raw, 0o600); err != nil {
		panic(err)
	}
}
