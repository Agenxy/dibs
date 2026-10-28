// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"runtime"
	"strings"

	"github.com/agenxy/dibs/internal/core"
)

// normalizeSeparators folds Windows path separators to `/` in every path an
// op carries, on Windows only. The GOOS check is the impure input; its
// outcome is what gets ledgered, never re-decided at replay.
func normalizeSeparators(op *core.Op) {
	if !foldsSeparators {
		return
	}
	op.Path = foldSeparators(op.Path)
	for i, d := range op.Dirs {
		op.Dirs[i] = foldSeparators(d)
	}
	if op.Agent != nil {
		op.Agent.CWD = foldSeparators(op.Agent.CWD)
		op.Agent.RepoDir = foldSeparators(op.Agent.RepoDir)
		op.Agent.RepoRoot = foldSeparators(op.Agent.RepoRoot)
	}
}

// foldsSeparators is whether this daemon folds `\` to `/`: Windows, and
// nowhere else, since a backslash is an ordinary character in a unix
// filename. A variable so a test can exercise the fold on the machine it
// runs on.
var foldsSeparators = runtime.GOOS == "windows"

// foldSeparators is the fold itself, for the QUERIES as well as the ops:
// the guard compares a path it is asked about against claims the fold
// stored with `/`, and a query left with `\` matched nothing, so an
// exclusive claim on `C:/repo` let `C:\repo\file.go` through. Round eleven
// of the pre-release review.
func foldSeparators(p string) string {
	if !foldsSeparators {
		return p
	}
	return strings.ReplaceAll(p, "\\", "/")
}
