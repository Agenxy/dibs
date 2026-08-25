package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A dangling symlink is not an absence, and ensureCA must not treat it as one.
//
// os.Stat follows links, so a link whose target is gone reports ErrNotExist. An
// interrupted restore that leaves one dangling link and one missing file made
// BOTH halves look absent: ensureCA found nothing to refuse and generated a NEW
// signing identity, locking out every machine that had run `dibs trust` while
// reporting itself healthy.
//
// The reasoning was already written down in this package, in
// preflightCertReadable, which calls Lstat and says so at length. It was
// applied in one of the two places that need it. The dangling-symlink test
// drives that implementation and never reaches this one, so it stayed green
// while startup behaved differently. Found by the pre-release review.
//
// This test drives ensureCA itself, which is the whole point of it existing.
func TestEnsureCARefusesADanglingHalf(t *testing.T) {
	dir := t.TempDir()
	caCert := filepath.Join(dir, "ca-cert.pem")
	caKey := filepath.Join(dir, "ca-key.pem")

	// One dangling link, one genuinely absent file: what a half-finished
	// restore leaves behind.
	if err := os.Symlink(filepath.Join(dir, "gone.pem"), caCert); err != nil {
		t.Fatal(err)
	}
	// Setup must hold: Stat has to report the link as absent, or this test is
	// not exercising the case it names.
	if _, err := os.Stat(caCert); err == nil {
		t.Fatal("setup: the symlink resolves, so it is not dangling")
	}
	if _, err := os.Lstat(caCert); err != nil {
		t.Fatal("setup: the symlink itself is not there")
	}

	_, _, err := ensureCA(caCert, caKey)
	if err == nil {
		t.Fatal("ensureCA generated a NEW signing identity beside a dangling half. " +
			"Every machine pinned to the old one is locked out by a daemon that " +
			"then reports itself healthy, which is the silent rotation this file " +
			"refuses three other ways")
	}
	if !strings.Contains(err.Error(), caCert) {
		t.Errorf("the refusal does not name the file that needs attention: %v", err)
	}
	// And nothing was written: a refusal that still generated a key would have
	// done the damage it was refusing.
	if _, statErr := os.Lstat(caKey); statErr == nil {
		t.Error("a key was written despite the refusal")
	}
}

// And a genuinely empty directory is still a first run, or no board could ever
// start for the first time.
func TestEnsureCAStillGeneratesOnAFirstRun(t *testing.T) {
	dir := t.TempDir()
	cert, key, err := ensureCA(filepath.Join(dir, "ca-cert.pem"), filepath.Join(dir, "ca-key.pem"))
	if err != nil {
		t.Fatalf("a first run was refused: %v", err)
	}
	if cert == nil || key == nil {
		t.Error("a first run produced no signing identity")
	}
}
