// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package selfupdate

import "testing"

func TestGuestMemberNamesOnlyPublishedTargets(t *testing.T) {
	for _, target := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		got, err := GuestMemberName(target[0], target[1])
		want := "members/" + target[0] + "_" + target[1] + "/dibs"
		if err != nil || got != want {
			t.Fatalf("%v: %q, %v; want %q", target, got, err, want)
		}
	}
	for _, target := range [][2]string{{"darwin", "amd64"}, {"windows", "amd64"}, {"linux", "386"}, {"../linux", "amd64"}} {
		if name, err := GuestMemberName(target[0], target[1]); err == nil || name != "" {
			t.Fatalf("unsupported target %v admitted as %q", target, name)
		}
	}
}
