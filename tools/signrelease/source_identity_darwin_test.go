// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	if record := os.Getenv("DIBS_TEST_SOURCE_SIGN_RECORD"); record != "" && filepath.Base(os.Args[0]) == "codesign" {
		body, err := json.Marshal(os.Args[1:])
		if err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(record, body, 0o600); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	if os.Getenv("DIBS_TEST_SOURCE_SIGN_COMMAND") == "1" {
		main()
		os.Exit(0)
	}
}

// Enter at the command, including flag parsing. The external codesign fixture
// records argv; this proves channel selection, not an Apple signature.
func TestSourceSigningIsExplicitAndCannotChangeTheReleaseChannel(t *testing.T) {
	bin := t.TempDir()
	image, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dibd", "codesign"} {
		if err := os.WriteFile(filepath.Join(bin, name), image, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(identityEnv, "1111111111111111111111111111111111111111")
	t.Setenv(keychainEnv, "")
	t.Setenv("DIBS_TEST_SOURCE_SIGN_COMMAND", "1")
	record := filepath.Join(bin, "record.json")
	t.Setenv("DIBS_TEST_SOURCE_SIGN_RECORD", record)
	for _, tc := range []struct {
		name   string
		flags  []string
		reject bool
	}{
		{"release", nil, true}, {"source", []string{"-source-identity"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.flags...), "-sign", filepath.Join(bin, "dibd"))
			out, err := exec.Command(os.Args[0], args...).CombinedOutput()
			if (err != nil) != tc.reject {
				t.Fatalf("channel rejection=%v want %v: %v %s", err != nil, tc.reject, err, out)
			}
			if tc.reject {
				if _, err := os.Stat(record); !os.IsNotExist(err) {
					t.Fatal("release called codesign with an unpinned identity")
				}
				return
			}
			body, err := os.ReadFile(record)
			if err != nil || !strings.Contains(string(body), os.Getenv(identityEnv)) {
				t.Fatalf("source identity did not reach signer: %v %s", err, body)
			}
		})
	}
}
