package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type publicationFixture struct {
	proof           publicationProof
	meta            map[string]any
	stage           string
	status          releaseStatus
	jobs, body      []byte
	signature       []byte
	missingEvidence bool
	next            runner
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Network I/O and subprocess verdicts are the only seams. Signature argument
// checks are wiring evidence; no fixture verdict is called crypto acceptance.
func newPublicationFixture(t *testing.T, c *config, next runner) *publicationFixture {
	t.Helper()
	old := rehearsalRepository
	rehearsalRepository = "Agenxy/dibs-private-test-fixture"
	t.Cleanup(func() { rehearsalRepository = old })
	c.publicationRun = "456"
	d, err := rehearsalTarget(*c)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := sourceObjects(context.Background(), *c, command)
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	s := fixtureAssets(t, *c, stage)
	s.Tag, s.ID = d.tag, 789
	sums, err := assetDigests(*c, stage)
	if err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	f := &publicationFixture{
		proof: publicationProof{
			Schema: 1, Scope: publicationArtifact, Repository: d.repository,
			RunID: c.publicationRun, Attempt: "2", WorkflowSHA: c.sha, Version: c.version, SHA: c.sha,
			Tag: d.tag, EvidenceTag: evidenceTag(*c, "2"), Identity: d.identity(), ReleaseID: s.ID, Objects: objects, Assets: sums,
			Negative: &no, DraftFound: &yes, Uploaded: &yes, Readback: &yes, Immutable: &yes, Readonly: &yes, DryPlans: &yes,
		},
		meta: map[string]any{
			"id": 456, "run_attempt": 2, "event": "workflow_dispatch", "status": "completed", "conclusion": "success",
			"head_sha": c.sha, "path": d.workflow, "head_branch": d.tag, "repository": map[string]any{"full_name": d.repository},
		},
		stage: stage, status: s, next: next, signature: []byte("fixture receipt signature"), jobs: []byte(`{"total_count":1,"jobs":[{"name":"full-publication","status":"completed","conclusion":"success"}]}`),
	}
	f.seal(t)
	base := *c
	c.publicClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "" {
			t.Fatal("public proof used a credential or mutation")
		}
		var body []byte
		var err error
		code := http.StatusOK
		prefix := "https://api.github.com/repos/" + d.repository
		assetPrefix := "https://github.com/" + d.repository + "/releases/download/"
		switch r.URL.String() {
		case prefix + "/actions/runs/456":
			body, err = json.Marshal(f.meta)
		case prefix + "/actions/runs/456/attempts/2/jobs?per_page=100":
			body = f.jobs
		case prefix + "/releases/tags/" + d.tag:
			body, err = json.Marshal(f.status)
		case prefix + "/releases/tags/" + evidenceTag(base, "2"):
			if f.missingEvidence {
				code = http.StatusNotFound
				break
			}
			body, err = json.Marshal(releaseStatus{ID: 790, Tag: evidenceTag(base, "2"), Immutable: true, Assets: []struct{ Name string }{{publicationFile}, {publicationBundle}}})
		case assetPrefix + evidenceTag(base, "2") + "/" + publicationFile:
			body = f.body
		case assetPrefix + evidenceTag(base, "2") + "/" + publicationBundle:
			body = f.signature
		default:
			if strings.HasPrefix(r.URL.String(), assetPrefix+d.tag+"/") {
				body, err = os.ReadFile(filepath.Join(stage, filepath.Base(r.URL.Path)))
			} else {
				t.Fatalf("unexpected public HTTP door: %s", r.URL)
			}
		}
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
	return f
}

func (f *publicationFixture) seal(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.proof)
	if err != nil {
		t.Fatal(err)
	}
	f.body = data
}

func (f *publicationFixture) runner(t *testing.T, c config) runner {
	t.Helper()
	d, err := rehearsalTarget(c)
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if name == "cosign" && len(args) > 0 && args[0] == "verify-blob" {
			if len(args) != 10 || args[6] != "--certificate-identity" || args[7] != d.identity() || args[4] != "--trusted-root" ||
				filepath.Base(args[5]) != "trusted-root.json" || len(env) != 3 || env[0] != "TUF_MIRROR=" || env[1] != "TUF_ROOT_JSON=" || !strings.Contains(env[2], "unused-cache") {
				t.Fatalf("incorrect closed scratch verifier: %v %v", args, env)
			}
			for _, path := range []string{args[1], args[3], args[5]} {
				if data, err := os.ReadFile(path); err != nil || len(data) == 0 {
					t.Fatalf("verifier missing owned evidence/root: %s %v", path, err)
				}
			}
			return nil, nil
		}
		if f.next != nil {
			return f.next(ctx, env, name, args...)
		}
		return nil, fmt.Errorf("unexpected effect: %s %v", name, args)
	}
}

func withPublicationFixture(t *testing.T, c *config, next runner) runner {
	t.Helper()
	f := newPublicationFixture(t, c, next)
	return f.runner(t, *c)
}
