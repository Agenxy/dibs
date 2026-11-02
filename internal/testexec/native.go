// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package testexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/procexec"
)

var nativeFixture struct {
	once  sync.Once
	image []byte
	err   error
}

// NativeFixtureImage builds this test package once without race instrumentation
// for a PURE external OS double. Its entry point must execute no product code.
// Drivers exercising product APIs must keep the current race-instrumented image.
// A Go race exit grace is not part of a native helper's lifecycle: inheriting a
// driver's stderr during that grace otherwise invents a retained-pipe failure.
// Each caller receives its own bytes; mutating one cannot corrupt later doubles.
func NativeFixtureImage(t testing.TB) []byte {
	t.Helper()
	nativeFixture.once.Do(func() {
		path := filepath.Join(t.TempDir(), "native-fixture")
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		cmd := procexec.Command(ctx, "go", "test", "-c", "-race=false", "-o", path, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			nativeFixture.err = fmt.Errorf("build pure external native fixture: %w: %s", err, out)
			return
		}
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			nativeFixture.err = err
			return
		}
		nativeFixture.image, nativeFixture.err = root.ReadFile("native-fixture")
		nativeFixture.err = errors.Join(nativeFixture.err, root.Close())
	})
	if nativeFixture.err != nil {
		t.Fatal(nativeFixture.err)
	}
	return bytes.Clone(nativeFixture.image)
}
