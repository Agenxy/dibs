// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/selfupdate"
)

// Process-door wiring proof, NOT a crypto proof. The real cosign policy stays
// untouched; this executable checks its arguments and controlled exit verdict.
func TestMain(m *testing.M) {
	if os.Getenv("DIBS_TEST_RELEASE_GC_HOOK") == "1" {
		os.Exit(releaseFixtureGCHook())
	}
	if os.Getenv("DIBS_TEST_RELEASE_COSIGN") == "1" {
		os.Exit(cosignFixture())
	}
	os.Exit(m.Run())
}

func cosignFixture() int {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		return 0
	}
	if len(os.Args) != 11 || os.Args[1] != "verify-blob" {
		return 20
	}
	flags := map[string]string{}
	for i := 3; i+1 < len(os.Args); i += 2 {
		flags[os.Args[i]] = os.Args[i+1]
	}
	if flags["--certificate-identity"] != "https://github.com/Agenxy/dibs/.github/workflows/release.yml@refs/tags/v0.0.11" ||
		flags["--certificate-oidc-issuer"] != "https://token.actions.githubusercontent.com" ||
		flags["--trusted-root"] == "" || os.Getenv("TUF_MIRROR") != "" || os.Getenv("TUF_ROOT_JSON") != "" {
		return 21
	}
	for _, path := range []string{os.Args[2], flags["--bundle"], flags["--trusted-root"]} {
		b, err := os.ReadFile(path)
		if err != nil || len(b) == 0 {
			return 22
		}
	}
	if os.Getenv("DIBS_TEST_RELEASE_COSIGN_REFUSE") == "true" {
		return 23
	}
	return 0
}

func useCosignFixture(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "cosign"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DIBS_TEST_RELEASE_COSIGN", "1")
}

func fixtureAssets(t *testing.T, c config, dir string) releaseStatus {
	t.Helper()
	s := releaseStatus{Tag: "v" + c.version, Immutable: true}
	write(t, filepath.Join(dir, selfupdate.ChecksumsName), "")
	for _, name := range assets(c.version) {
		s.Assets = append(s.Assets, struct{ Name string }{name})
		if name != selfupdate.ChecksumsName {
			write(t, filepath.Join(dir, name), "fixture "+name)
		}
	}
	if err := completeChecksums(c, dir); err != nil {
		t.Fatal(err)
	}
	return s
}

func copyFixture(t *testing.T, source, dest string, c config) {
	t.Helper()
	for _, name := range assets(c.version) {
		b, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dest, name), string(b))
	}
}

func TestPublicReleaseRetryOnlyVerifiesNeverBuildsSignsOrWrites(t *testing.T) {
	for _, mode := range []string{"valid", "tampered", "missing", "signature-refused", "mutable-public"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t)
			useCosignFixture(t)
			stage := t.TempDir()
			s := fixtureAssets(t, c, stage)
			if mode == "mutable-public" {
				s.Immutable = false
			}
			if mode == "tampered" {
				write(t, filepath.Join(stage, "dibs.rb"), "tampered")
			}
			if mode == "missing" {
				s.Assets = s.Assets[1:]
			}
			if mode == "signature-refused" {
				t.Setenv("DIBS_TEST_RELEASE_COSIGN_REFUSE", "true")
			}
			run := func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
				if name == "gh" && len(args) > 0 && args[0] == "api" {
					return json.Marshal([]releaseStatus{s})
				}
				if name == "gh" && len(args) > 2 && args[0] == "release" && args[1] == "download" {
					copyFixture(t, stage, args[6], c)
					return nil, nil
				}
				t.Fatalf("public retry tried a mutation/build: %s %v", name, args)
				return nil, nil
			}
			err := publish(context.Background(), c, run)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}

func TestDraftCannotBecomePublicBeforeUploadedBytesVerify(t *testing.T) {
	for _, mode := range []string{"valid", "build-fail", "bad-upload", "became-public"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t)
			useCosignFixture(t)
			if err := localTag(context.Background(), c, command); err != nil {
				t.Fatal(err)
			}
			git(t, "push", "origin", "refs/tags/v0.0.11")
			var s releaseStatus
			s.Tag = "v0.0.11"
			s.Draft = true
			created, uploaded, edited, verifiedReadback := false, false, false, false
			run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
				if name == "git" {
					return command(ctx, env, name, args...)
				}
				if name == "goreleaser" {
					if mode == "build-fail" {
						return nil, errors.New("build refused")
					}
					s = fixtureAssets(t, c, "dist")
					s.Draft = true
					write(t, "dist/homebrew/Casks/dibs.rb", "fixture dibs.rb")
					return nil, nil
				}
				if name == "go" || name == "cosign" {
					return nil, nil
				}
				if name == "gh" && args[0] == "api" {
					if !created {
						return []byte(`[]`), nil
					}
					if mode == "became-public" {
						s.Draft = false
					}
					return json.Marshal([]releaseStatus{s})
				}
				if name == "gh" && args[0] == "release" {
					switch args[1] {
					case "create":
						created = true
						return nil, nil
					case "upload":
						uploaded = true
						return nil, nil
					case "download":
						if !uploaded {
							t.Fatal("readback before upload")
						}
						copyFixture(t, "dist", args[6], c)
						if mode == "bad-upload" {
							write(t, filepath.Join(args[6], "dibs.rb"), "damaged upload")
						}
						verifiedReadback = true
						return nil, nil
					case "edit":
						if !verifiedReadback {
							t.Fatal("un-drafted before readback")
						}
						edited = true
						s.Draft = false
						s.Immutable = true
						return nil, nil
					}
				}
				t.Fatalf("unexpected command %s %v", name, args)
				return nil, nil
			}
			err := publish(context.Background(), c, run)
			if (err == nil) != (mode == "valid") || edited != (mode == "valid") {
				t.Fatalf("%s: err%v edited%v", mode, err, edited)
			}
			if mode == "became-public" && uploaded {
				t.Fatal("clobbered a public release")
			}
		})
	}
}

func TestChecksumsPreserveMemberProofsAndRejectAmbiguousProducerEntries(t *testing.T) {
	c := fixture(t)
	dir := t.TempDir()
	fixtureAssets(t, c, dir)
	path := filepath.Join(dir, selfupdate.ChecksumsName)
	member := strings.Repeat("a", 64) + "  dibs_linux_arm64/dibs\n"
	write(t, path, member)
	if err := completeChecksums(c, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), member) {
		t.Fatal("discarded packaged member proof")
	}
	for _, bad := range []string{"bad  dibs.rb\n", member + member} {
		write(t, path, bad)
		if err := completeChecksums(c, dir); err == nil {
			t.Fatal("ambiguous checksums accepted")
		}
	}
}
