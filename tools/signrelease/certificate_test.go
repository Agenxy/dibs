// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func init() {
	if filepath.Base(os.Args[0]) == "security" && os.Getenv("DIBS_TEST_SIGNER_ROOT") != "" {
		fmt.Println("SHA-1 hash: " + os.Getenv("DIBS_TEST_SIGNER_ROOT"))
		os.Exit(0)
	}
}

func TestImportedSigningCertificateCannotRotateSilently(t *testing.T) {
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "security"), image, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		name, hash string
		reject     bool
	}{
		{"stable", "27113117309b43e4748ed37d7002d1122a7af9ae", false},
		{"rotated", "1111111111111111111111111111111111111111", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DIBS_TEST_SIGNER_ROOT", tc.hash)
			got, err := certificateIn("fixture-not-a-real-keychain")
			if (err != nil) != tc.reject || (!tc.reject && got != tc.hash) {
				t.Fatalf("imported certificate rejection=%v, want %v: %s %v", err != nil, tc.reject, got, err)
			}
		})
	}
}
