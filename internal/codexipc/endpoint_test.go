// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestNativeEndpointRefusesGroupOrOtherAccessBeforeInput(t *testing.T) {
	for _, name := range []string{"directory", "socket"} {
		t.Run(name, func(t *testing.T) {
			s := testcodexipc.Start(t, "idle", nil)
			path := filepath.Join(os.Getenv("CODEX_HOME"), "ipc")
			if name == "socket" {
				path = filepath.Join(path, "ipc.sock")
			}
			if err := os.Chmod(path, 0o777); err != nil {
				t.Fatal("setup permissions:", err)
			}
			_, err := Deliver(context.Background(), testcodexipc.Thread, "private metadata")
			if err == nil || len(s.Inputs()) != 0 {
				t.Fatal("unsafe endpoint accepted input")
			}
		})
	}
}
