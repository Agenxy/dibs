package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type faultGuestExportRoot struct {
	*os.Root
	fault          string
	linked, opened bool
}

func (r *faultGuestExportRoot) Link(old, name string) error {
	if r.fault == "link" {
		return os.ErrPermission
	}
	if err := r.Root.Link(old, name); err != nil {
		return err
	}
	r.linked = true
	return nil
}

func (r *faultGuestExportRoot) Open(name string) (*os.File, error) {
	if name != "." || !r.linked {
		return nil, errors.New("directory operation must follow real publication")
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
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func TestGuestExportRequiresExclusivePublicationAndDirectoryDurability(t *testing.T) {
	for _, fault := range []string{"", "link", "open", "sync"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			fs := &faultGuestExportRoot{Root: root, fault: fault}
			body := []byte("fixture-private-material")
			err = writeGuestRecipeExclusive(fs, "recipe.json", body)
			if (err == nil) != (fault == "") {
				t.Fatalf("durability failure reported as success: %v", err)
			}
			published, readErr := os.ReadFile(filepath.Join(dir, "recipe.json"))
			if fault == "link" {
				if fs.linked || !os.IsNotExist(readErr) {
					t.Fatal("failed exclusive link published a file")
				}
			} else {
				if !fs.linked || !fs.opened || readErr != nil || !bytes.Equal(published, body) {
					t.Fatal("real publication/directory path was not tested or ambiguous final file was destroyed")
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".guest-export-") {
					t.Fatal("temporary private material was left behind")
				}
			}
		})
	}
}

func TestGuestExportPreservesLateCompetitorAndSymlink(t *testing.T) {
	for _, mode := range []string{"file", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "recipe.json")
			export, err := openGuestRecipeExport(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = export.root.Close() }()
			original := []byte("retained-original")
			if mode == "file" {
				if err := os.WriteFile(path, original, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(dir, "original.json")
				if err := os.WriteFile(target, original, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeGuestRecipeExclusive(export.root, export.name, []byte("replacement")); err == nil {
				t.Fatal("file arriving after preflight was overwritten")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("retained recipe or symlink target changed")
			}
			if _, err := openGuestRecipeExport(path); err == nil {
				t.Fatal("existing destination passed preflight")
			}
		})
	}
}

func TestGuestExportConcurrentWritersHaveOneWinner(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := writeGuestRecipeExclusive(root, "recipe.json", []byte("complete-private-fixture")); err == nil {
				successes.Add(1)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("exclusive publication had more or fewer than one successful writer")
	}
	body, err := os.ReadFile(filepath.Join(dir, "recipe.json"))
	if err != nil || string(body) != "complete-private-fixture" {
		t.Fatal("published recipe is partial")
	}
}

func TestGuestExportRechecksDirectoryPrivacyAfterMint(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	export, err := openGuestRecipeExport(filepath.Join(dir, "recipe.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = export.root.Close() }()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeGuestRecipeExclusive(export.root, export.name, []byte("private")); err == nil {
		t.Fatal("directory privacy changed during issuance and credentials were still exported")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("unsafe directory received secret material")
	}
}
