// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package paths

import (
	"errors"
	"os"
	"testing"
)

func TestMalformedServicePinNeverFallsBack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DIBS_DIR", "")
	if err := WriteServiceDirectory(t.TempDir()); err != nil {
		t.Fatal("setup:", err)
	}
	file, err := ServiceConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"[broken", "version=2\ndata_dir='/existing'", "version=1\ndata_dir='relative'",
		"version=1\ndata_dir='/existing'\nfuture_pin=true",
	} {
		t.Run(body, func(t *testing.T) {
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			selected, err := SelectDir(false)
			var invalid *ServiceConfigError
			if !errors.As(err, &invalid) || selected.Dir != "" {
				t.Fatalf("broken pin selected another board: %v %v", selected, err)
			}
			defer func() {
				if _, ok := recover().(*ServiceConfigError); !ok {
					t.Error("legacy string-only lookup did not refuse with a configuration error")
				}
			}()
			_ = DataDir()
		})
	}
}

func TestRequiredServicePinAndPrivateAtomicWrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DIBS_DIR", "")
	if selected, err := SelectDir(true); err == nil || selected.Dir != "" {
		t.Fatalf("missing required pin selected another board: %v %v", selected, err)
	}
	dir := t.TempDir()
	if err := WriteServiceDirectory(dir); err != nil {
		t.Fatal(err)
	}
	file, _ := ServiceConfigPath()
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pin not private: %v %v", info, err)
	}
	selected, err := SelectDir(true)
	if err != nil || selected.Dir != dir || selected.Source != file {
		t.Fatalf("pin write not consumed by shared resolver: %v %v", selected, err)
	}
	if err := WriteServiceDirectory("relative"); err == nil || DataDir() != dir {
		t.Fatal("invalid replacement changed the existing service pin")
	}
}
