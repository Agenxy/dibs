// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build unix

package main

import "syscall"

// The private-directory preflight correctly refuses a group-writable
// ancestor, and t.TempDir's subdirectories take the process umask. Under the
// user-private-group default of 002 (Debian-family accounts, the K3 seat
// among them) every fixture directory was refused, and six doctor tests
// failed for a reason that had nothing to do with what they test. Fixtures
// get the conventional 022 so the product check stays strict.
func init() { syscall.Umask(0o022) }
