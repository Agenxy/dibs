package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The opt-in probe runs the unchanged GoReleaser signing command against this
// test binary as a recording cosign stand-in. It proves the bytes/argv that
// reach signing, NOT a Sigstore signature, workflow identity or published tag.
func TestMain(m *testing.M) {
	if os.Getenv("DIBS_SIGNING_DOOR_CHILD") == "1" && len(os.Args) > 1 && os.Args[1] == "sign-blob" {
		if len(os.Args) != 6 || os.Args[2] != "--yes" || os.Args[3] != "--bundle" ||
			filepath.Base(os.Args[4]) != "checksums.txt.bundle" || filepath.Base(os.Args[5]) != "checksums.txt" {
			os.Exit(2)
		}
		input, err := os.ReadFile(os.Args[5])
		if err != nil {
			os.Exit(3)
		}
		if err = os.WriteFile(os.Getenv("DIBS_SIGNING_DOOR_RECEIPT"), input, 0o600); err != nil {
			os.Exit(4)
		}
		// GoReleaser requires a bundle artifact to exist. This is labelled dry-run
		// material and is never passed to a verifier or an installation path.
		if err = os.WriteFile(os.Args[4], []byte("dry-run: not a Sigstore bundle\n"), 0o600); err != nil {
			os.Exit(5)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestChecksumSigningDoor(t *testing.T) {
	if os.Getenv("DIBS_TEST_RELEASE_SIGN_DOOR") != "1" {
		t.Skip("opt-in actual release/signing dry run; ordinary archive gate checks member bytes")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("release pipeline requires the native Swift/macOS tools")
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(self, filepath.Join(dir, "cosign")); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(dir, "signing-input")
	cmd := exec.Command("goreleaser", "release", "--snapshot", "--clean", "--skip=publish,announce,validate,homebrew")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DIBS_SIGNING_DOOR_CHILD=1", "DIBS_SIGNING_DOOR_RECEIPT="+receipt)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("actual release signing door: %v\n%s", err, out)
	}
	input, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal("signer never received the checksums:", err)
	}
	final, err := os.ReadFile(filepath.Join(root, "dist", "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input, final) {
		t.Fatal("published checksum bytes differ from the signing input")
	}
	if err := checkMembers(root); err != nil {
		t.Fatal("signing input does not match all actual packaged executables:", err)
	}
}
