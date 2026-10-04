package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func proofFixture(c config, rehearsal bool) receipt {
	return receipt{
		Schema: 1, Repository: repository, RunID: c.runID, Attempt: c.attempt,
		WorkflowSHA: c.workflowSHA, Version: c.version, SHA: c.sha,
		Tree: strings.Repeat("b", 40), TagOID: strings.Repeat("c", 40), Rehearsal: &rehearsal,
		PublicationRun: c.publicationRun,
	}
}

// Only network/subprocess effects are replaced. Authentication and all receipt
// parsing take the production execute/authenticatedReceipt path.
func receiptRunner(t *testing.T, r receipt, jobs string, next runner) runner {
	t.Helper()
	return func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if name == "gh" && len(args) == 2 && args[0] == "api" {
			switch args[1] {
			case "repos/" + repository + "/actions/runs/123":
				return json.Marshal(map[string]any{
					"id": 123, "run_attempt": 1, "event": "workflow_dispatch",
					"head_branch": "main", "head_sha": r.WorkflowSHA, "path": workflowPath,
					"status": "completed", "conclusion": "success", "repository": map[string]any{"full_name": repository},
				})
			case "repos/" + repository + "/actions/runs/123/attempts/1/jobs?per_page=100":
				return []byte(jobs), nil
			case "repos/" + repository + "/actions/runs/123/artifacts?per_page=100":
				return []byte(`{"total_count":1,"artifacts":[{"id":1,"name":"release-preflight-1","expired":false}]}`), nil
			}
		}
		if name == "gh" && strings.HasPrefix(joined, "run download 123 --repo "+repository+" --name release-preflight-1 --dir ") {
			data, err := json.Marshal(r)
			if err != nil {
				return nil, err
			}
			return nil, os.WriteFile(filepath.Join(args[len(args)-1], "dibs-preflight.json"), data, 0o600)
		}
		if next != nil {
			return next(ctx, env, name, args...)
		}
		return nil, errors.New("unexpected effect: " + name + " " + joined)
	}
}

const passedJob = `{"total_count":1,"jobs":[{"name":"preflight","status":"completed","conclusion":"success"}]}`

func TestAuthenticatedHandoffRoutesRehearsalOnlyToReadOnlyReceiver(t *testing.T) {
	for _, rehearsal := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "rehearsal"}[rehearsal], func(t *testing.T) {
			c := fixture(t)
			c.phase = "finalize"
			if !rehearsal {
				c.publicationRun = "456"
			}
			r := proofFixture(c, rehearsal)
			var dispatched []string
			run := receiptRunner(t, r, passedJob, func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
				if name != "gh" {
					t.Fatalf("unexpected executable: %s", name)
				}
				dispatched = args
				return nil, nil
			})
			if err := execute(context.Background(), c, run); err != nil {
				t.Fatal(err)
			}
			ref, mode := "v0.0.11", "publish-only"
			if rehearsal {
				ref, mode = "main", "delivery-rehearsal"
			}
			want := "workflow run release.yml --repo " + repository + " --ref " + ref + " -f mode=" + mode + " -f version=0.0.11 -f sha=" + c.sha + " -f preflight_run=123"
			if !rehearsal {
				want += " -f full_publication_run=456"
			}
			if strings.Join(dispatched, " ") != want {
				t.Fatalf("wrong handoff: %v", dispatched)
			}
			if rehearsal {
				c.phase = "delivery-rehearsal"
				if err := execute(context.Background(), c, receiptRunner(t, r, passedJob, nil)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRehearsalAndMissingReceiptFlagCannotCreateTagSignOrPublish(t *testing.T) {
	for _, flag := range []string{"true", "null", "missing", "wrong", "false-oversized"} {
		t.Run(flag, func(t *testing.T) {
			c := fixture(t)
			r := proofFixture(c, true)
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			s := string(data)
			switch flag {
			case "null":
				s = strings.ReplaceAll(s, `"rehearsal":true`, `"rehearsal":null`)
			case "missing":
				s = strings.ReplaceAll(s, `,"rehearsal":true`, "")
			case "wrong":
				s = strings.ReplaceAll(s, `"rehearsal":true`, `"rehearsal":"false"`)
			case "false-oversized":
				s = strings.ReplaceAll(s, `"rehearsal":true`, `"rehearsal":false`) + strings.Repeat(" ", 8192)
			}
			write(t, c.receiptPath, s)
			c.phase = "commit-tag"
			noEffects := func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
				t.Fatalf("untrusted receipt reached an effect: %s %v", name, args)
				return nil, nil
			}
			if err := execute(context.Background(), c, noEffects); err == nil {
				t.Fatal("unsafe receipt accepted")
			}
			if got := git(t, "ls-remote", "--tags", "origin"); got != "" {
				t.Fatal("unsafe receipt wrote tag")
			}
			// The public entry point must refuse TRUE before any git/sign/build.
			if flag == "true" {
				t.Setenv("GITHUB_REF", "refs/tags/v0.0.11")
				for _, phase := range []string{"authorize-receipt", "publish", "cask"} {
					c.phase = phase
					if err := execute(context.Background(), c, receiptRunner(t, r, passedJob, noEffects)); err == nil || !strings.Contains(err.Error(), "NEVER sign or publish") {
						t.Fatalf("%s: %v", phase, err)
					}
				}
			}
		})
	}
}

func TestReceiptAttemptBindingAndReceiverFinalizerDoesNotLoop(t *testing.T) {
	c := fixture(t)
	r := proofFixture(c, false)
	r.Attempt = "2"
	if _, err := authenticatedReceipt(context.Background(), "123", receiptRunner(t, r, passedJob, nil)); err == nil {
		t.Fatal("artifact from different attempt accepted")
	}
	c.phase = "finalize"
	jobs := `{"total_count":1,"jobs":[{"name":"delivery-rehearsal","status":"completed","conclusion":"success"}]}`
	if err := execute(context.Background(), c, receiptRunner(t, r, jobs, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestAnnotatedTagObjectIsIdenticalAcrossIndependentJobs(t *testing.T) {
	c := fixture(t)
	if err := localTag(context.Background(), c, command); err != nil {
		t.Fatal(err)
	}
	want := git(t, "rev-parse", "refs/tags/v0.0.11")
	origin := git(t, "remote", "get-url", "origin")
	second := filepath.Join(t.TempDir(), "other-job")
	git(t, "clone", "--branch", "main", origin, second)
	t.Chdir(second)
	if got := git(t, "tag", "--list"); got != "" {
		t.Fatalf("setup inherited local tag: %q", got)
	}
	if err := localTag(context.Background(), c, command); err != nil {
		t.Fatal(err)
	}
	if got := git(t, "rev-parse", "refs/tags/v0.0.11"); got != want {
		t.Fatalf("tag objects differ: %s != %s", got, want)
	}
}

func TestPublisherAuthenticatesExactReceiptCommitTreeAndTagObject(t *testing.T) {
	for _, mode := range []string{"exact", "wrong-tree", "wrong-tag-object", "wrong-candidate"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t)
			if err := localTag(context.Background(), c, command); err != nil {
				t.Fatal(err)
			}
			git(t, "push", "origin", "refs/tags/v0.0.11")
			proofRun := withPublicationFixture(t, &c, command)
			r := proofFixture(c, false)
			r.Tree = git(t, "rev-parse", "HEAD^{tree}")
			r.TagOID = git(t, "rev-parse", "refs/tags/v0.0.11")
			switch mode {
			case "wrong-tree":
				r.Tree = strings.Repeat("d", 40)
			case "wrong-tag-object":
				r.TagOID = strings.Repeat("e", 40)
			case "wrong-candidate":
				r.SHA = strings.Repeat("f", 40)
			}
			t.Setenv("GITHUB_REF", "refs/tags/v0.0.11")
			c.phase = "authorize"
			err := execute(context.Background(), c, receiptRunner(t, r, passedJob, proofRun))
			if (err == nil) != (mode == "exact") {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}
