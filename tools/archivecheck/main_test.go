package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

type file struct {
	body []byte
	mode int64
}

func writeArchive(t *testing.T, path string, files map[string]file) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, fl := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: fl.mode, Size: int64(len(fl.body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(fl.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

// A required path is one the runtime executes, so "present" is not the bar:
// it has to be a Mach-O image for this Mac, carried with its execute bit. A
// script under the binary's name and a binary without the bit both used to
// pass as "not a binary, nothing to check". Found by the pre-release review,
// round four.
func TestARequiredExecutableMustBeARunnableMachO(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs a Mach-O image to put in the archive: this test binary")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dibd", "dibs"}
	dir := t.TempDir()
	// SIGNED with the identifiers a release carries, because the check now
	// includes them and a fixture that cannot pass the sound case turns every
	// refusal below into a false positive. `-` is ad-hoc, which is all this
	// needs: the identifier is what is being asserted, not the certificate.
	signed := func(name, identifier string) []byte {
		t.Helper()
		at := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(at, image, 0o700); err != nil {
			t.Fatal(err)
		}
		out, cerr := exec.Command("codesign", "--force", "--identifier", identifier,
			"--sign", "-", "--timestamp=none", at).CombinedOutput()
		if cerr != nil {
			t.Fatalf("codesign %s: %v\n%s", name, cerr, out)
		}
		b, rerr := os.ReadFile(at)
		if rerr != nil {
			t.Fatal(rerr)
		}
		return b
	}
	dibdImage := signed("dibd", "org.agenxy.dibs")
	dibsImage := signed("dibs", "org.agenxy.dibs.cli")
	archive := func(tag string, files map[string]file) string {
		p := filepath.Join(dir, "dibs_0.0.7_"+tag+"_darwin_"+runtime.GOARCH+".tar.gz")
		writeArchive(t, p, files)
		return p
	}

	sound := archive("sound", map[string]file{
		"dibd": {dibdImage, 0o755}, "dibs": {dibsImage, 0o755},
	})
	if err := carries(sound, want); err != nil {
		t.Fatal("setup: a sound archive was refused, so the refusals below prove nothing:", err)
	}

	script := archive("script", map[string]file{
		"dibd": {[]byte("#!/bin/sh\nexit 0\n"), 0o755}, "dibs": {dibsImage, 0o755},
	})
	if err := carries(script, want); err == nil {
		t.Error("a shell script under dibd's name passed the check: it is in the file " +
			"listing, and every install that runs it fails")
	}

	inert := archive("inert", map[string]file{
		"dibd": {dibdImage, 0o644}, "dibs": {dibsImage, 0o755},
	})
	if err := carries(inert, want); err == nil {
		t.Error("dibd carried with mode 0644 passed the check: present in the listing, " +
			"and exec refuses it on every machine")
	}
}
