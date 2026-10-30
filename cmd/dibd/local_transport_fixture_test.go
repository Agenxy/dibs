// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"os"
	"testing"
)

// Private and short enough for a Unix socket, while also compiling on the
// platforms whose CI currently checks daemon source without running it.
func privateDaemonFixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dibs-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
