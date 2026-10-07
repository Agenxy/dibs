// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"errors"
	"os"
	"testing"
)

type faultGuestNonceRoot struct {
	*os.Root
	fault   string
	renamed bool
	opened  bool
}

func (r *faultGuestNonceRoot) Rename(old, name string) error {
	if err := r.Root.Rename(old, name); err != nil {
		return err
	}
	r.renamed = true
	return nil
}

func (r *faultGuestNonceRoot) Open(name string) (*os.File, error) {
	if name != "." || !r.renamed {
		return nil, errors.New("directory operation must follow the real rename")
	}
	r.opened = true
	if r.fault == "open" {
		return nil, os.ErrPermission
	}
	f, err := r.Root.Open(name)
	if err != nil {
		return nil, err
	}
	if r.fault == "sync" {
		// A closed descriptor makes File.Sync actually fail. No fake Sync
		// function that could pass while production silently skips it.
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func TestGuestNonceRequiresDirectoryDurabilityBeforeReturning(t *testing.T) {
	for _, fault := range []string{"", "open", "sync"} {
		t.Run("directory-"+fault, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			fs := &faultGuestNonceRoot{Root: root, fault: fault}
			nonce, err := writeGuestNonce(fs, "guest.nonce")
			if !fs.renamed || !fs.opened {
				t.Fatal("real write/rename path returned without synchronizing its directory")
			}
			if fault == "" {
				if err != nil || len(nonce) != 64 {
					t.Fatalf("durable private nonce failed: %v", err)
				}
			} else if err == nil || nonce != "" {
				t.Fatal("failed directory operation released a nondurable credential to register")
			}
		})
	}
}
