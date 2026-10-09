//go:build !unix

// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import "os"

// This Unix protocol has not been measured on other platforms. An absent
// endpoint retains the old route; a present unsupported one fails explicitly.
func ownedByUser(_ os.FileInfo) bool { return false }
