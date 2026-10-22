// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package paths

import (
	"os"
	"testing"
)

func TestServicePathIsPreservedWithoutChangingTheReadersEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DIBS_DIR", "")
	before := os.Getenv("PATH")
	for _, source := range []string{"migrated", "configured"} {
		for _, path := range []string{"/custom tools:/usr/bin", ""} {
			if err := WriteServiceSettings(t.TempDir(), &path, source); err != nil {
				t.Fatal(err)
			}
			selected, err := SelectDir(true)
			if err != nil {
				t.Fatal(err)
			}
			value, from := selected.EffectiveServicePath()
			if value != path || from != source || os.Getenv("PATH") != before {
				t.Fatalf("service PATH changed or affected reader: %q %q %q", value, from, os.Getenv("PATH"))
			}
		}
	}
	if err := WriteServiceDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	selected, err := SelectDir(true)
	if err != nil {
		t.Fatal(err)
	}
	if value, source := selected.EffectiveServicePath(); value != DefaultServicePath || source != "default" {
		t.Fatalf("default service PATH: %q %q", value, source)
	}
	bad := "/bin\x00/usr/bin"
	if err := WriteServiceSettings(t.TempDir(), &bad, "configured"); err == nil {
		t.Fatal("invalid environment path was written")
	}
}
