// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package selfupdate

import "testing"

func TestGuestBridgeRequiresExactStableReleaseOnceFloorIsSet(t *testing.T) {
	for _, version := range []string{"", "devel", "devel+abcdef", "0.0.9-rc.1", "0.0.10-0.20261003000000-abcdef012345", "0.0.9+dirty", " 0.0.9", "0.0.09", "0.0.8", "0.0.9, 99.0.0"} {
		if err := CheckGuestBridgeVersion(version, "v0.0.9"); err == nil {
			t.Fatalf("unsupported bridge %q admitted", version)
		}
		if err := CheckGuestBridgeVersion(version, ""); err != nil {
			t.Fatal("unset floor unexpectedly gated existing invitation transport")
		}
	}
	for _, version := range []string{"0.0.9", "v0.0.9", "0.0.10"} {
		if err := CheckGuestBridgeVersion(version, "v0.0.9"); err != nil {
			t.Fatalf("stable supporting bridge %q refused: %v", version, err)
		}
	}
	if err := CheckGuestBridgeVersion("0.0.9", "invalid"); err == nil {
		t.Fatal("invalid compiled floor failed open")
	}
}
