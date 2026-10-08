// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationEvidenceMustAuthenticateEveryScopeAndOutcome(t *testing.T) {
	no := false
	mutations := map[string]func(*publicationFixture){
		"failed run":          func(f *publicationFixture) { f.meta["conclusion"] = "failure" },
		"wrong run":           func(f *publicationFixture) { f.meta["id"] = 457 },
		"wrong repository":    func(f *publicationFixture) { f.meta["repository"] = map[string]any{"full_name": repository} },
		"wrong workflow":      func(f *publicationFixture) { f.meta["path"] = workflowPath },
		"main ref":            func(f *publicationFixture) { f.meta["head_branch"] = "main" },
		"different candidate": func(f *publicationFixture) { f.meta["head_sha"] = strings.Repeat("a", 40) },
		"failed latest attempt": func(f *publicationFixture) {
			f.jobs = []byte(`{"total_count":1,"jobs":[{"name":"full-publication","status":"completed","conclusion":"failure"}]}`)
		},
		"incomplete jobs": func(f *publicationFixture) {
			f.jobs = []byte(`{"total_count":2,"jobs":[{"name":"full-publication","status":"completed","conclusion":"success"}]}`)
		},
		"missing evidence release": func(f *publicationFixture) { f.missingEvidence = true },
		"different signed run":     func(f *publicationFixture) { f.proof.RunID = "457"; f.seal(t) },
		"different evidence tag":   func(f *publicationFixture) { f.proof.EvidenceTag += "-wrong"; f.seal(t) },
		"old receipt attempt":      func(f *publicationFixture) { f.proof.Attempt = "1"; f.seal(t) },
		"production identity": func(f *publicationFixture) {
			f.proof.Identity = productionTarget(config{version: "0.0.11"}).identity()
			f.seal(t)
		},
		"negative control":        func(f *publicationFixture) { yes := true; f.proof.Negative = &yes; f.seal(t) },
		"missing negative flag":   func(f *publicationFixture) { f.proof.Negative = nil; f.seal(t) },
		"no draft readback":       func(f *publicationFixture) { f.proof.Readback = &no; f.seal(t) },
		"missing retry outcome":   func(f *publicationFixture) { f.proof.Readonly = nil; f.seal(t) },
		"changed workflow blob":   func(f *publicationFixture) { f.proof.Objects[workflowPath] = strings.Repeat("a", 40); f.seal(t) },
		"changed tool tree":       func(f *publicationFixture) { f.proof.Objects["tools/releaseflow"] = strings.Repeat("a", 40); f.seal(t) },
		"changed whole tree":      func(f *publicationFixture) { f.proof.Objects["tree"] = strings.Repeat("a", 40); f.seal(t) },
		"missing input":           func(f *publicationFixture) { delete(f.proof.Objects, "go.mod"); f.seal(t) },
		"changed asset digest":    func(f *publicationFixture) { f.proof.Assets["dibs.rb"] = strings.Repeat("a", 64); f.seal(t) },
		"mutable release":         func(f *publicationFixture) { f.status.Immutable = false },
		"different release":       func(f *publicationFixture) { f.status.ID++ },
		"duplicate release asset": func(f *publicationFixture) { f.status.Assets[0] = f.status.Assets[1] },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			c := fixture(t)
			c.phase = "validate"
			f := newPublicationFixture(t, &c, command)
			mutate(f)
			if err := execute(context.Background(), c, f.runner(t, c)); err == nil {
				t.Fatal("invalid full-publication proof accepted")
			}
			if git(t, "tag", "--list") != "" || git(t, "ls-remote", "--tags", "origin") != "" {
				t.Fatal("invalid evidence created a tag")
			}
		})
	}
}

func TestUnsignedOrWrongIdentityReceiptCannotAuthorizeProduction(t *testing.T) {
	for _, reason := range []string{"unsigned receipt", "different signing identity", "different signing ref"} {
		t.Run(reason, func(t *testing.T) {
			c := fixture(t)
			c.phase = "validate"
			f := newPublicationFixture(t, &c, command)
			base := f.runner(t, c)
			run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
				if name == "cosign" && len(args) > 1 && args[0] == "verify-blob" && filepath.Base(args[1]) == publicationFile {
					return nil, errors.New(reason)
				}
				return base(ctx, env, name, args...)
			}
			if err := execute(context.Background(), c, run); err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("receipt did not reach exact signature refusal %q: %v", reason, err)
			}
			if git(t, "tag", "--list") != "" || git(t, "ls-remote", "--tags", "origin") != "" {
				t.Fatal("untrusted signed receipt created a tag")
			}
		})
	}
}

func TestSignedReceiptParsingRemainsStrictAndBounded(t *testing.T) {
	for _, body := range []string{`{"schema":1,"unknown":true}`, `{} {}`, strings.Repeat(" ", 32*1024+1)} {
		path := filepath.Join(t.TempDir(), publicationFile)
		write(t, path, body)
		if _, err := readPublication(path); err == nil {
			t.Fatal("untrusted or oversized receipt accepted")
		}
	}
}

func TestPublicProofIsBoundedAtTheRealHTTPDoor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("public fetch sent credentials")
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 4097)))
	}))
	defer server.Close()
	c := config{publicClient: server.Client()}
	if out, err := publicBytes(context.Background(), c, server.URL, 4096); err == nil || len(out) != 0 {
		t.Fatal("oversized public proof collected")
	}
	path := filepath.Join(t.TempDir(), "owned-download")
	if err := downloadPublicFile(context.Background(), c, server.URL, path, 4096); err == nil {
		t.Fatal("oversized streamed asset accepted")
	}
	if info, err := os.Stat(path); err != nil || info.Size() > 4097 {
		t.Fatal("stream ignored asset bound")
	}
}

func TestCommitTagReauthenticatesLatestPublicationAttempt(t *testing.T) {
	c := fixture(t)
	f := newPublicationFixture(t, &c, command)
	if err := localTag(context.Background(), c, command); err != nil {
		t.Fatal(err)
	}
	r := proofFixture(c, false)
	r.Tree = git(t, "rev-parse", "HEAD^{tree}")
	r.TagOID = git(t, "rev-parse", "refs/tags/v"+c.version)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	write(t, c.receiptPath, string(data))
	c.phase = "commit-tag"
	f.meta["run_attempt"] = 3
	f.meta["conclusion"] = "failure"
	if err = execute(context.Background(), c, f.runner(t, c)); err == nil {
		t.Fatal("commit inherited old successful attempt")
	}
	if git(t, "ls-remote", "--tags", "origin") != "" {
		t.Fatal("failed reauthentication pushed tag")
	}
}

func TestReadonlyRetryAllowsNoBuildSigningOrMutation(t *testing.T) {
	run := readOnlyPublication(func(context.Context, []string, string, ...string) ([]byte, error) {
		t.Fatal("mutation reached subprocess")
		return nil, nil
	})
	for _, argv := range [][]string{{"gh", "release", "upload"}, {"gh", "api", "repos/example/thing", "--method", "POST"}, {"goreleaser", "release"}, {"cosign", "sign-blob"}, {"git", "push"}} {
		if _, err := run(context.Background(), nil, argv[0], argv[1:]...); err == nil {
			t.Fatalf("mutation allowed: %v", argv)
		}
	}
}
