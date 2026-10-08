// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The helper runs through the actual executable lookup/initialize/cache-read
// door, rather than calling the comparison directly. It is NOT a cryptographic
// proof: the real cosign/network-denied release-bundle measurement is separate.
func TestMain(m *testing.M) {
	if os.Getenv("DIBS_TEST_COSIGN_RECORD") != "" {
		os.Exit(recordCosignHelper())
	}
	if mode := os.Getenv("DIBS_TEST_COSIGN_ROOT"); mode != "" {
		rootCosignHelper(mode)
		return
	}
	os.Exit(m.Run())
}

func rootCosignHelper(mode string) {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		return
	}
	if len(os.Args) != 4 || os.Args[1] != "initialize" || os.Args[2] != "--mirror" ||
		os.Args[3] != sigstoreTUFMirror || os.Getenv("TUF_MIRROR") != "" || os.Getenv("TUF_ROOT_JSON") != "" {
		os.Exit(10)
	}
	cache := os.Getenv("TUF_ROOT")
	if cache == "" || cache == os.Getenv("DIBS_TEST_INHERITED_CACHE") {
		os.Exit(11)
	}
	if mode == "fail" {
		os.Exit(12)
	}
	data := []byte(sigstoreTrustedRoot)
	switch mode {
	case "changed":
		data = append(data, '\n')
	case "oversize":
		data = []byte(strings.Repeat("x", maxTrustedRoot+1))
	case "missing":
		return
	}
	dir := filepath.Join(cache, "tuf-repo-cdn.sigstore.dev", "targets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		os.Exit(13)
	}
	if err := os.WriteFile(filepath.Join(dir, "trusted_root.json"), data, 0o600); err != nil {
		os.Exit(14)
	}
}

func TestEmbeddedSigstoreRootHasFrozenPin(t *testing.T) {
	// Deliberate rotation changes BOTH the reviewed bytes and this frozen pin.
	// Do not mechanically update this guard when changing the embedded file.
	const want = "6494e21ea73fa7ee769f85f57d5a3e6a08725eae1e38c755fc3517c9e6bc0b66"
	sum := sha256.Sum256([]byte(sigstoreTrustedRoot))
	if got := hex.EncodeToString(sum[:]); got != want || sigstoreTrustedRootSHA256 != want {
		t.Fatalf("embedded root pin changed: bytes=%s declared=%s want=%s", got, sigstoreTrustedRootSHA256, want)
	}
}

func TestPinnedSigstoreRootReturnsOwnedSameTrustBytes(t *testing.T) {
	root, err := PinnedSigstoreRoot()
	if err != nil {
		t.Fatal(err)
	}
	if string(root) != sigstoreTrustedRoot {
		t.Fatal("shared verifier root differs from installed trust")
	}
	root[0] ^= 1
	fresh, err := PinnedSigstoreRoot()
	if err != nil || string(fresh) != sigstoreTrustedRoot {
		t.Fatal("caller modified embedded authority")
	}
}

func TestReleaseRootGateUsesFreshAuthenticatedProductionTUF(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	name := "cosign"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err = os.WriteFile(filepath.Join(bin, name), data, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	// A developer's custom cache/mirror/root must not define release trust.
	t.Setenv("TUF_ROOT", "/untrusted-inherited-cache")
	t.Setenv("DIBS_TEST_INHERITED_CACHE", "/untrusted-inherited-cache")
	t.Setenv("TUF_MIRROR", "https://untrusted.invalid")
	t.Setenv("TUF_ROOT_JSON", "/untrusted-root.json")
	for _, tc := range []struct{ mode, refusal string }{
		{"same", ""},
		{"changed", "rotate"},
		{"oversize", "too large"},
		{"missing", "trusted_root.json"},
		{"fail", "initialize"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Setenv("DIBS_TEST_COSIGN_ROOT", tc.mode)
			err := CheckCurrentTrustedRoot(context.Background())
			if tc.refusal == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.refusal) {
				t.Fatalf("mode %s: got %v; want refusal containing %q", tc.mode, err, tc.refusal)
			}
		})
	}
}

func TestTagWorkflowChecksCurrentRootBeforePublishing(t *testing.T) {
	b, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "\n  release:\n")
	end := strings.Index(s, "\n  registry:\n")
	if start < 0 || end <= start {
		t.Fatal("publication job boundary missing")
	}
	s = s[start:end]
	guard := strings.Index(s, "run: go run ./tools/sigstore-root-check")
	cosign := strings.Index(s, "uses: sigstore/cosign-installer@")
	publish := strings.Index(s, "run: go run ./tools/releaseflow -phase publish")
	if guard < 0 || cosign < 0 || publish < 0 || guard <= cosign || guard >= publish {
		t.Fatal("tag workflow must check authenticated root after installing cosign and before publishing")
	}
}

func TestReleasePreparationChecksRootBeforeClaimingVersion(t *testing.T) {
	b, err := os.ReadFile("../../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "\n  release:\n")
	end := strings.Index(s, "\n  ci:\n")
	if start < 0 || end < start {
		t.Fatal("release preparation and ci tasks missing")
	}
	prep := s[start:end]
	guard := strings.Index(prep, "go run ./tools/sigstore-root-check")
	stamp := strings.Index(prep, "go run ./tools/version -set")
	if guard < 0 || stamp < 0 || guard >= stamp {
		t.Fatal("release preparation must check authenticated root before claiming a version")
	}
	if strings.Contains(s[end:], "go run ./tools/sigstore-root-check") {
		t.Fatal("ordinary ci must not require current Sigstore network access")
	}
}
