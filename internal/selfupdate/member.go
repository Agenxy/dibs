// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package selfupdate

import "fmt"

// The first three targets predate RISC-V release archives and remain required
// when reading older handoffs. RISC-V requires both signed digest entries.
var guestReleaseTargets = [...][2]string{
	{"darwin", "arm64"},
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"linux", "riscv64"},
}

const requiredGuestTargets = 3

// GuestMemberName is the checksum key for the packaged CLI, not a downloadable
// asset or a second manifest. The signed tag supplies its version. These are
// exactly the published guest-provisioning targets; a cross-build is not one.
func GuestMemberName(goos, goarch string) (string, error) {
	for _, target := range guestReleaseTargets {
		if goos == target[0] && goarch == target[1] {
			return "members/" + goos + "_" + goarch + "/dibs", nil
		}
	}
	return "", fmt.Errorf("no published guest CLI member for %s/%s", goos, goarch)
}
