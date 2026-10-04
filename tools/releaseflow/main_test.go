package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/release"
	"github.com/agenxy/dibs/internal/selfupdate"
)

func fixture(t *testing.T) config {
	t.Helper()
	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	if _, err := command(context.Background(), nil, "git", "init", "--bare", origin); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	git(t, "init", "-b", "main")
	git(t, "config", "user.name", "Release fixture")
	git(t, "config", "user.email", "fixture@example.invalid")
	git(t, "config", "commit.gpgsign", "false")
	git(t, "config", "tag.gpgsign", "false")
	write(t, release.Changelog, "## [Unreleased]\n\n## [0.0.11] - 2026-10-03\n\nFixture\n")
	for _, path := range release.Manifests {
		write(t, path, `{"version":"0.0.11"}`)
	}
	for _, path := range publicationObjects {
		if strings.HasPrefix(path, "tools/") || strings.HasPrefix(path, "cmd/") || strings.HasPrefix(path, "internal/") {
			write(t, filepath.Join(path, "fixture.txt"), "source fixture: "+path)
		} else {
			write(t, path, "source fixture: "+path)
		}
	}
	git(t, "add", ".")
	git(t, "commit", "-m", "fixture")
	git(t, "remote", "add", "origin", origin)
	git(t, "push", "-u", "origin", "main")
	sha := git(t, "rev-parse", "HEAD")
	t.Setenv("GITHUB_EVENT_NAME", "workflow_dispatch")
	t.Setenv("GITHUB_REF", "refs/heads/main")
	t.Setenv("GITHUB_REPOSITORY", repository)
	return config{
		version: "0.0.11", sha: sha, phase: "preflight", receiptPath: filepath.Join(dir, "dibs-preflight.json"),
		runID: "123", attempt: "1", workflowSHA: sha,
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := command(context.Background(), nil, "git", args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestFailingPreflightCannotCreateRemoteTagOrReceipt(t *testing.T) {
	c := fixture(t)
	before := git(t, "ls-remote", "--tags", "origin")
	var calls []string
	run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if name == "git" {
			return command(ctx, env, name, args...)
		}
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "mise" && strings.Join(args, " ") == "exec -- task ci" {
			return nil, errors.New("deliberate task ci failure")
		}
		return nil, errors.New("must not reach any post-gate command")
	}
	run = withPublicationFixture(t, &c, run)
	if err := execute(context.Background(), c, run); err == nil || !strings.Contains(err.Error(), "deliberate task ci failure") {
		t.Fatalf("wrong failure: %v", err)
	}
	if after := git(t, "ls-remote", "--tags", "origin"); after != before {
		t.Fatalf("FAILED gate wrote remote tags: before=%q after=%q", before, after)
	}
	if git(t, "tag", "--list", "v0.0.11") != "v0.0.11" {
		t.Fatal("setup did not reach the local-tag production door")
	}
	if _, err := os.Stat(c.receiptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed gate left receipt: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("unexpected publication or later commands: %v", calls)
	}
}

func TestSuccessfulPreflightStillWritesNoRemoteTagUntilSeparateCommitJob(t *testing.T) {
	c := fixture(t)
	var calls []string
	run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if name == "git" {
			return command(ctx, env, name, args...)
		}
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "goreleaser" && (len(env) != 2 || env[0] != "DIBS_SNAPSHOT_RELEASE_VERSION=0.0.11" ||
			strings.Join(args, " ") != "release --snapshot --clean --skip=sign,publish,announce,validate") {
			t.Fatalf("not the exact-version offline snapshot: %v %v", env, args)
		}
		if name == "goreleaser" {
			write(t, "dist/homebrew/Casks/dibs.rb", "generated fixture cask")
			write(t, "dist/checksums.txt", "")
			for _, name := range assets(c.version) {
				if name != selfupdate.BundleName {
					write(t, filepath.Join("dist", name), "fixture "+name)
				}
			}
			write(t, "dist/checksums.txt", "")
			git(t, "status", "--porcelain")
		}
		return nil, nil
	}
	run = withPublicationFixture(t, &c, run)
	if err := execute(context.Background(), c, run); err != nil {
		t.Fatal(err)
	}
	if got := git(t, "ls-remote", "--tags", "origin"); got != "" {
		t.Fatalf("preflight pushed: %q", got)
	}
	want := []string{"mise exec -- task ci", "go run ./tools/sigstore-root-check", "goreleaser release --snapshot --clean --skip=sign,publish,announce,validate", "go run ./tools/archivecheck", "go run ./tools/mcpbundle -version 0.0.11"}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("missing gate/build/signing proof: %v", calls)
	}
	c.phase = "commit-tag"
	proofRun := withPublicationFixture(t, &c, command)
	if err := execute(context.Background(), c, proofRun); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(git(t, "ls-remote", "--tags", "origin", "refs/tags/v0.0.11^{}"), c.sha) {
		t.Fatal("successful separate commit job did not push exact candidate")
	}
	// Idempotent, but never with different provenance.
	if err := execute(context.Background(), c, proofRun); err != nil {
		t.Fatal(err)
	}
	c.sha = strings.Repeat("a", 40)
	if err := execute(context.Background(), c, command); err == nil {
		t.Fatal("mismatched candidate accepted")
	}
}

func TestRehearsalFailureAndWrongContextsHaveNoRemoteSideEffects(t *testing.T) {
	c := fixture(t)
	c.rehearsal = true
	if err := execute(context.Background(), c, command); err == nil || !strings.Contains(err.Error(), "deliberate preflight") {
		t.Fatalf("%v", err)
	}
	if git(t, "ls-remote", "--tags", "origin") != "" {
		t.Fatal("rehearsal wrote remote tag")
	}
	c.rehearsal = false
	t.Setenv("GITHUB_REF", "refs/heads/feature")
	if err := execute(context.Background(), c, command); err == nil {
		t.Fatal("branch dispatch accepted")
	}
	t.Setenv("GITHUB_REF", "refs/heads/main")
	t.Setenv("GITHUB_EVENT_NAME", "push")
	if err := execute(context.Background(), c, command); err == nil {
		t.Fatal("tag push accepted")
	}
}

func TestPreflightRejectsWrongVersionBeforeAnyGateOrTag(t *testing.T) {
	c := fixture(t)
	c.version = "0.0.12"
	if err := execute(context.Background(), c, command); err == nil || !strings.Contains(err.Error(), "changelog") {
		t.Fatalf("%v", err)
	}
	if git(t, "tag", "--list") != "" || git(t, "ls-remote", "--tags", "origin") != "" {
		t.Fatal("mismatched candidate created a tag")
	}
}

func TestAuthenticatedReceiptRefusesForgedAndUnsuccessfulOrigins(t *testing.T) {
	sha := strings.Repeat("a", 40)
	base := map[string]any{
		"id": 123, "run_attempt": 1, "event": "workflow_dispatch", "head_branch": "main", "head_sha": sha,
		"path": workflowPath, "status": "completed", "conclusion": "success", "repository": map[string]any{"full_name": repository},
	}
	for _, field := range []string{"conclusion", "head_branch", "event", "path", "repository", "status", "head_sha"} {
		t.Run(field, func(t *testing.T) {
			meta := make(map[string]any)
			for k, v := range base {
				meta[k] = v
			}
			meta[field] = "wrong"
			run := func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
				if name != "gh" || len(args) != 2 || args[0] != "api" {
					t.Fatalf("untrusted receipt reached another operation: %s %v", name, args)
				}
				return json.Marshal(meta)
			}
			if _, err := authenticatedReceipt(context.Background(), "123", run); err == nil {
				t.Fatal("untrusted origin accepted")
			}
		})
	}
}

func TestPublisherAuthRequiresExactCandidateAndTagContext(t *testing.T) {
	c := fixture(t)
	c.phase = "authorize"
	if err := execute(context.Background(), c, command); err == nil || !strings.Contains(err.Error(), "exact tag") {
		t.Fatalf("main signing context allowed: %v", err)
	}
	for _, bad := range []string{"--help", "0.0.011", "v0.0.11", "0.0.11\n"} {
		c.version = bad
		if err := execute(context.Background(), c, command); err == nil {
			t.Fatalf("invalid version %q accepted", bad)
		}
	}
}
