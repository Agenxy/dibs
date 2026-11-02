// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package adminpw

import (
	"strings"
	"testing"
)

func TestHashPinsProductionWorkFactor(t *testing.T) {
	const password = "production-work-factor-probe"
	encoded, err := Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately independent of iters: lowering production's cost must fail
	// this policy guard, not silently rewrite its expected value.
	if parts := strings.Split(encoded, "$"); len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "600000" {
		t.Fatalf("production password hash lost its 600000-round work factor: %q", encoded)
	}
	if !Verify(password, encoded) {
		t.Fatal("production verifier rejected its own production-cost hash")
	}
	if Verify(password+"-wrong", encoded) {
		t.Fatal("production verifier accepted a different password")
	}
}
