// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Command installstamp checks the built images before a source install can
// replace any installed binaries. No daemon, ledger or signing state is read.
package main

import (
	"debug/buildinfo"
	"fmt"
	"os"

	"github.com/agenxy/dibs/internal/build"
)

func main() {
	for _, path := range []string{"bin/dibs", "bin/dibd"} {
		info, err := buildinfo.ReadFile(path)
		if err == nil {
			err = build.CheckInstallStamp(info)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "installstamp: %s: %v\n", path, err)
			os.Exit(1)
		}
	}
}
