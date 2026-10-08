// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func guestFixtureChecksums() string {
	s := ""
	for _, target := range []string{"darwin_arm64", "linux_amd64", "linux_arm64"} {
		s += strings.Repeat("a", 64) + "  dibs_0.0.9_" + target + ".tar.gz\n"
		s += strings.Repeat("b", 64) + "  members/" + target + "/dibs\n"
	}
	return s
}

// Direct values below test detached projection and signed-input shape only,
// not signature cryptography. Actual admission always enters the offline door.
func TestGuestSnapshotRequiresEveryExactArchiveAndMemberDigest(t *testing.T) {
	v := VerifiedRelease{tag: "v0.0.9", checksums: guestFixtureChecksums()}
	snapshot, err := v.GuestSnapshot("v0.0.9", "devel+fixture")
	if err != nil {
		t.Fatal("sound snapshot setup failed:", err)
	}
	m := snapshot.Metadata()
	if m.Tag != "v0.0.9" || m.BoardBuild != "devel+fixture" || len(m.Assets) != 3 {
		t.Fatal("projection lost truthful provenance")
	}
	m.Assets[0].URL = "altered"
	if snapshot.Metadata().Assets[0].URL == "altered" {
		t.Fatal("projection was not detached")
	}
	for _, bad := range []string{
		strings.Replace(v.checksums, "members/darwin_arm64/dibs", "missing", 1),
		strings.Replace(v.checksums, "dibs_0.0.9_linux_amd64.tar.gz", "missing", 1),
		v.checksums + strings.Repeat("c", 64) + "  members/linux_arm64/dibs\n",
		strings.Replace(v.checksums, strings.Repeat("b", 64), strings.Repeat("z", 64), 1),
		v.checksums + "malformed\n",
	} {
		v.checksums = bad
		if _, err := v.GuestSnapshot("v0.0.9", "devel"); err == nil {
			t.Fatal("missing/duplicate/malformed evidence accepted")
		}
	}
}

func TestGuestMetadataRefusesInventedTargetsURLsAndDigests(t *testing.T) {
	s, err := (VerifiedRelease{tag: "v0.0.9", checksums: guestFixtureChecksums()}).GuestSnapshot("v0.0.9", "devel")
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*GuestReleaseMetadata){
		func(m *GuestReleaseMetadata) { m.Assets[0].URL = "https://evil.invalid/dibs" },
		func(m *GuestReleaseMetadata) { m.Assets[0].OS = "windows" },
		func(m *GuestReleaseMetadata) { m.Assets[0] = m.Assets[1] },
		func(m *GuestReleaseMetadata) { m.Assets[0].BinarySHA = strings.Repeat("z", 64) },
		func(m *GuestReleaseMetadata) { m.Tag = "latest" },
		func(m *GuestReleaseMetadata) { m.Status = "READY" },
		func(m *GuestReleaseMetadata) { m.BoardBuild = "" },
		func(m *GuestReleaseMetadata) { m.Assets = m.Assets[:2] },
	} {
		m := s.Metadata()
		edit(&m)
		if err := m.Validate(); err == nil {
			t.Fatal("invalid guest handoff metadata accepted")
		}
	}
}

func TestGuestEvidenceCacheKeysContentAndBuildNotFileTimestamp(t *testing.T) {
	v := fixtureRelease(t)
	dir := t.TempDir()
	saveFixtureRecord(t, v, dir)
	receipt := filepath.Join(t.TempDir(), "verifier-calls")
	t.Setenv("DIBS_TEST_COSIGN_RECORD_RECEIPT", receipt)
	var cache ReleaseEvidenceCache
	load := func(build string, want bool) {
		t.Helper()
		got, err := cache.Load(context.Background(), dir, build)
		if (err == nil) != want || (want && got.Tag() != v.Tag()) {
			t.Fatalf("cached load: %v %v", got, err)
		}
	}
	load("devel", true)
	load("devel", true)
	count := func(want int) {
		t.Helper()
		b, err := os.ReadFile(receipt)
		if err != nil || strings.Count(string(b), "verify\n") != want {
			t.Fatalf("verifier count want%d got%q %v", want, b, err)
		}
	}
	count(1)
	load("devel+newbuild", true)
	count(2)
	path := filepath.Join(dir, releaseRecordName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	r := releaseRecord{Tag: v.tag, Checksums: []byte("altered checksum bytes"), Bundle: []byte(v.bundle)}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	load("devel+newbuild", false)
	load("devel+newbuild", false)
	count(4) // failures are retried, never memoized as cryptographic verdicts
	// Restore the original content: it was not kept as an alternate authority
	// after withdrawal, so the changed content must be verified afresh.
	saveFixtureRecord(t, v, dir)
	load("devel+newbuild", true)
	count(5)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	load("devel+newbuild", false)
}

func TestGuestEvidenceCacheRecoversAfterVerifierRepairWithoutRecordChange(t *testing.T) {
	v := fixtureRelease(t)
	dir := t.TempDir()
	saveFixtureRecord(t, v, dir)
	verifierPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	var cache ReleaseEvidenceCache
	if _, err := cache.Load(context.Background(), dir, "devel"); err == nil || !strings.Contains(err.Error(), "cosign is not installed") {
		t.Fatalf("missing verifier was not diagnosed: %v", err)
	}
	t.Setenv("PATH", verifierPath)
	if got, err := cache.Load(context.Background(), dir, "devel"); err != nil || got.Tag() != v.Tag() {
		t.Fatalf("repair with unchanged evidence did not recover: %v", err)
	}
}

func TestGuestEvidenceCacheRetriesVerifierCrashAndUnclassifiedExit(t *testing.T) {
	for _, failure := range []string{"signal", "exit"} {
		t.Run(failure, func(t *testing.T) {
			v := fixtureRelease(t)
			dir := t.TempDir()
			saveFixtureRecord(t, v, dir)
			receipt := filepath.Join(t.TempDir(), "calls")
			t.Setenv("DIBS_TEST_COSIGN_RECORD_RECEIPT", receipt)
			t.Setenv("DIBS_TEST_COSIGN_RECORD_FAILURE", failure)
			var cache ReleaseEvidenceCache
			got, err := cache.Load(context.Background(), dir, "devel")
			var ended *exec.ExitError
			if got.Tag() != "" || !errors.As(err, &ended) {
				t.Fatalf("verifier failure setup did not run/refuse: %v", err)
			}
			if (failure == "exit" && ended.ExitCode() != 37) ||
				(failure == "signal" && runtime.GOOS != "windows" && ended.ExitCode() != -1) {
				t.Fatalf("wrong fixture termination: %v", err)
			}
			t.Setenv("DIBS_TEST_COSIGN_RECORD_FAILURE", "")
			if got, err = cache.Load(context.Background(), dir, "devel"); err != nil || got.Tag() != v.Tag() {
				t.Fatalf("verifier repair with unchanged bytes stranded the daemon: %v", err)
			}
			calls, err := os.ReadFile(receipt)
			if err != nil || string(calls) != "verify\nverify\n" {
				t.Fatalf("repair did not enter the verifier again: %q %v", calls, err)
			}
		})
	}
}

// Opt-in cryptographic measurement, run under an OS network-denying policy.
// The historical release is real signed evidence, NOT a supporting release:
// it predates member digests and must never produce admission metadata.
func TestGuestHistoricalSignedReleaseRefusesMissingMembers(t *testing.T) {
	fixture := os.Getenv("DIBS_TEST_SIGNED_RELEASE_FIXTURE_DIR")
	if fixture == "" {
		t.Skip("requires historical public signed fixture and real cosign")
	}
	checksums, err := os.ReadFile(filepath.Join(fixture, ChecksumsName))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(filepath.Join(fixture, BundleName))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	body, err := json.Marshal(releaseRecord{Tag: "v0.0.9", Checksums: checksums, Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, releaseRecordName)
	if err = os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var cache ReleaseEvidenceCache
	started := time.Now()
	v, err := cache.Load(ctx, dir, "devel")
	if err != nil {
		t.Fatal("historical signature setup failed:", err)
	}
	t.Logf("real offline admission signature verification: %s", time.Since(started))
	if _, err = v.GuestSnapshot("v0.0.9", "devel"); err == nil || !strings.Contains(err.Error(), "no exact archive/member digests") {
		t.Fatalf("historical evidence incorrectly admitted: %v", err)
	}
	// The production cache door must withdraw a previously verified value
	// after an exact-byte change, not keep projecting an earlier authority.
	body, err = json.Marshal(releaseRecord{Tag: "v0.0.9", Checksums: append(checksums, '\n'), Bundle: bundle})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := cache.Load(ctx, dir, "devel"); err == nil || got.Tag() != "" {
		t.Fatal("altered signed evidence retained cached authority")
	}
}

// A static property gets a shape guard: the retired cmd helper/floor must not
// coexist with the shared rule. This is not a claim to prove runtime wiring.
func TestGuestSelectionHasOneProductionDefinition(t *testing.T) {
	var selectors, floors int
	for _, dir := range []string{"../../cmd/dibs", ".", "../invites"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if fn, ok := n.(*ast.FuncDecl); ok && (fn.Name.Name == "SelectGuestRelease" || fn.Name.Name == "guestReleaseSelection") {
					selectors++
					if filepath.Base(file) != "guest.go" {
						t.Errorf("guest selection copy in %s", file)
					}
				}
				if spec, ok := n.(*ast.ValueSpec); ok {
					for _, name := range spec.Names {
						if name.Name == "GuestSupportingMinimum" || name.Name == "guestSupportingMinimum" {
							floors++
							if filepath.Base(file) != "guest.go" {
								t.Errorf("guest floor copy in %s", file)
							}
						}
					}
				}
				return true
			})
		}
	}
	if selectors != 1 || floors != 1 {
		t.Fatalf("expected one guest selector/floor, got %d/%d", selectors, floors)
	}
}
