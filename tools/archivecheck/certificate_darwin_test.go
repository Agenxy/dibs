// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Model codesign's external diagnostic contract, not a certificate generated
// in the operator's keychain. The actual archive reader/identity door runs.
func init() {
	if filepath.Base(os.Args[0]) != "codesign" || os.Getenv("DIBS_TEST_ARCHIVE_CERTIFICATE") == "" {
		return
	}
	if strings.Contains(strings.Join(os.Args[1:], " "), "--verify") {
		os.Exit(0)
	}
	fmt.Println("Identifier=org.agenxy.dibs.daemon")
	fmt.Println("designated => " + os.Getenv("DIBS_TEST_ARCHIVE_CERTIFICATE"))
	os.Exit(0)
}

func TestArchiveRefusesRotatedReleaseCertificate(t *testing.T) {
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codesign"), image, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	archive := filepath.Join(t.TempDir(), "dibs_0.0.16_darwin_"+runtime.GOARCH+".tar.gz")
	writeArchive(t, archive, map[string]file{"dibd": {image, 0o755}})
	const pinned = `identifier "org.agenxy.dibs.daemon" and certificate root = H"27113117309b43e4748ed37d7002d1122a7af9ae"`
	for _, tc := range []struct {
		name, requirement string
		reject            bool
	}{
		{"stable", pinned, false},
		{"rotated", strings.ReplaceAll(pinned, "27113117309b43e4748ed37d7002d1122a7af9ae", "1111111111111111111111111111111111111111"), true},
		{"weakened", pinned + " or true", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DIBS_TEST_ARCHIVE_CERTIFICATE", tc.requirement)
			err := carries(archive, []string{"dibd"})
			if (err != nil) != tc.reject {
				t.Fatalf("archive certificate rejection=%v, want %v: %v", err != nil, tc.reject, err)
			}
		})
	}
}
