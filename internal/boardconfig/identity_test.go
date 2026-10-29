// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package boardconfig

import "testing"

func TestBoardFingerprintDerivation(t *testing.T) {
	const want = "e93fa3a75ad5321600570e56b1b96d32"
	if got := BoardFingerprint("example-board"); got != want {
		t.Fatalf("domain-separated SHA-256 first 16 bytes: got %q, want %q", got, want)
	}
	if got := BoardFingerprint(""); got != "" {
		t.Fatalf("missing identity became comparison evidence: %q", got)
	}
}
