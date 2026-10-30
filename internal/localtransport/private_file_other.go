// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

//go:build !darwin && !linux

package localtransport

import "os"

// ReadPrivateFile retains the platform's existing TLS file access. No Unix
// ownership/mode/ACL qualification is claimed on this platform.
func ReadPrivateFile(path string) ([]byte, error) { return os.ReadFile(path) }
