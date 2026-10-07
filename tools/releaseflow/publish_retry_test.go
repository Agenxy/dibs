package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPublishEditRetryRequiresVerifiedStillDraftReadback(t *testing.T) {
	for _, mode := range []string{"500-then-success", "500-then-500", "500-already-public", "403", "500-changed-assets", "500-unknown-state"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t)
			c.publicationRun = "456"
			useCosignFixture(t)
			if err := localTag(context.Background(), c, command); err != nil {
				t.Fatal(err)
			}
			git(t, "push", "origin", "refs/tags/v0.0.11")
			s := fixtureAssets(t, c, "dist")
			s.Draft = true
			edits, downloads := 0, 0
			run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
				if name == "git" {
					return command(ctx, env, name, args...)
				}
				if name == "gh" && args[0] == "api" {
					if edits > 0 && mode == "500-unknown-state" {
						return nil, errors.New("GitHub status unavailable")
					}
					return json.Marshal([]releaseStatus{s})
				}
				if name == "gh" && args[0] == "release" {
					switch args[1] {
					case "upload":
						return nil, nil
					case "download":
						downloads++
						copyFixture(t, "dist", args[6], c)
						if edits > 0 && mode == "500-changed-assets" {
							write(t, args[6]+"/dibs.rb", "changed after first edit")
						}
						return nil, nil
					case "edit":
						edits++
						if downloads < edits {
							t.Fatal("publish edit without a fresh verified draft readback")
						}
						if mode == "403" {
							return nil, errors.New("HTTP 403: forbidden")
						}
						if mode == "500-already-public" {
							s.Draft, s.Immutable = false, true
							return nil, errors.New("HTTP 500: response lost after publication")
						}
						if edits == 1 || mode == "500-then-500" {
							return nil, errors.New("HTTP 500: internal server error")
						}
						s.Draft, s.Immutable = false, true
						return nil, nil
					}
				}
				t.Fatalf("unexpected command %s %v", name, args)
				return nil, nil
			}
			err := publishDraft(context.Background(), c, run)
			wantEdits := 1
			if mode == "500-then-success" || mode == "500-then-500" {
				wantEdits = 2
			}
			if edits != wantEdits {
				t.Fatalf("%s: edit calls=%d, want %d; err=%v", mode, edits, wantEdits, err)
			}
			if mode == "500-then-success" || mode == "500-already-public" {
				if err != nil {
					t.Fatalf("verified retry did not publish: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted an unpublished or unverified release")
			}
			if mode == "500-then-500" || mode == "403" {
				for _, part := range []string{
					"still a draft", "mode=publish-only", "version=0.0.11", "sha=" + c.sha,
					"preflight_run=123", "full_publication_run=456",
				} {
					if !strings.Contains(err.Error(), part) {
						t.Fatalf("missing %q in %v", part, err)
					}
				}
				if strings.Contains(err.Error(), "published release reports immutable:false") {
					t.Fatalf("false immutability-setting advice: %v", err)
				}
			}
		})
	}
}
