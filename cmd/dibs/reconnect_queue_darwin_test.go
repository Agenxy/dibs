// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
	"github.com/agenxy/dibs/internal/testexec"
)

var reconnectQueue struct {
	once sync.Once
	path string
	err  error
}

// Queue code is immutable, while CODEX_HOME, pending.json, app ancestry and all
// bridge processes remain private to each real reconnect contract. TestMain
// owns this directory until every contract and its children have finished.
func reconnectQueueFixture(t *testing.T) string {
	t.Helper()
	reconnectQueue.once.Do(func() {
		dir := filepath.Join(serviceFixtureRoot, "reconnect-queue")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			reconnectQueue.err = err
			return
		}
		// Production identifies the queue protocol by this executable name.
		reconnectQueue.path = filepath.Join(dir, "codex")
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		cmd := procexec.Command(ctx, "go", "build", "-o", reconnectQueue.path, "../../internal/wakeexec/testdata/codexqueue")
		cmd.Dir = serviceFixtureSource
		if out, err := cmd.CombinedOutput(); err != nil {
			reconnectQueue.err = fmt.Errorf("build reconnect queue fixture: %w: %s", err, out)
			return
		}
		if err := os.Chmod(reconnectQueue.path, 0o500); err != nil {
			reconnectQueue.err = err
			return
		}
		testexec.PrepareExecutable(t, exec.Command(reconnectQueue.path, "--version"))
	})
	if reconnectQueue.err != nil {
		t.Fatal(reconnectQueue.err)
	}
	return reconnectQueue.path
}
