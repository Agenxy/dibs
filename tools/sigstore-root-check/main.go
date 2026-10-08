// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Command sigstore-root-check refuses to publish with stale embedded trust.
// It authenticates the current production Sigstore root through normal TUF;
// it does not overwrite the source, rotate trust, install anything or publish.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/agenxy/dibs/internal/selfupdate"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sigstore-root-check:", err)
		os.Exit(1)
	}
	fmt.Println("sigstore-root-check: authenticated current root matches embedded pin")
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return selfupdate.CheckCurrentTrustedRoot(ctx)
}
