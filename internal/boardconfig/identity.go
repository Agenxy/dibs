// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import (
	"crypto/sha256"
	"encoding/hex"
)

// BoardFingerprint lets a local CLI compare boards after an authentication
// refusal without disclosing the node id, which may also identify this machine
// to other services. It is diagnostic evidence, never authentication.
func BoardFingerprint(node string) string {
	if node == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("dibs-board-identity\x00" + node))
	return hex.EncodeToString(sum[:16])
}
