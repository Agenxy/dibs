// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// A built child is not a Go test process. It must inherit the test's
// forbid-open environment and receive an error from the production opener.
package main

import (
	"fmt"
	"os"

	"github.com/agenxy/dibs/internal/harnessenv"
)

func main() {
	if os.Getenv("DIBS_TEST_FORBID_APP_OPEN") != "1" {
		fmt.Fprintln(os.Stderr, "refusing to probe the opener without its test guard")
		os.Exit(2)
	}
	// Invalid thread ID: even an old child without the guard refuses this
	// before it can execute /usr/bin/open. The assertion distinguishes the
	// inherited test refusal from ordinary URL validation.
	err := harnessenv.RealShower.Open([]string{"/usr/bin/open", "-g", "codex://threads/not-a-uuid"})
	if err == nil {
		fmt.Fprintln(os.Stderr, "real app opener unexpectedly succeeded")
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, err)
}
