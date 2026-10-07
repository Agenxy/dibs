// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin

package paths

func nativeSpelling(p string) string { return p }
