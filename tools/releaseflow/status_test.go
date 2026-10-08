// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReleaseStatusFindsDraftThroughTheListDoor(t *testing.T) {
	c := config{version: "0.0.11"}
	run := func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
		if name != "gh" || len(args) != 2 || args[0] != "api" || args[1] != "repos/Agenxy/dibs/releases?per_page=100&page=1" {
			t.Fatalf("draft discovery used the wrong API door: %s %v", name, args)
		}
		// Measured GitHub contract: listing includes a draft, but the get-by-tag
		// endpoint returns HTTP 404 for the very same existing draft.
		return []byte(`[{"id":402967536,"tag_name":"v0.0.11","draft":true,"immutable":false,"assets":[]}]`), nil
	}
	s, exists, err := status(context.Background(), c, run)
	if err != nil || !exists || s.Tag != "v0.0.11" || !s.Draft || s.Immutable || len(s.Assets) != 0 {
		t.Fatalf("lost the existing empty draft: %+v exists=%v err=%v", s, exists, err)
	}
}

func TestReleaseStatusScansAllBoundedPagesBeforeTrustingAMatch(t *testing.T) {
	for _, mode := range []string{"later-draft", "duplicate-same-page", "duplicate-later-page", "exact-tag", "absent", "full-bound", "later-error", "malformed", "null", "null-entry", "missing-tag", "object", "oversized-page"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			match := releaseStatus{Tag: "v0.0.11", Draft: true}
			run := func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
				calls++
				want := fmt.Sprintf("repos/Agenxy/dibs/releases?per_page=100&page=%d", calls)
				if name != "gh" || len(args) != 2 || args[0] != "api" || args[1] != want {
					t.Fatalf("not bounded sequential list discovery: %s %v", name, args)
				}
				if calls > 10 {
					t.Fatal("release scan exceeded its bound")
				}
				if mode == "later-error" && calls == 2 {
					return nil, errors.New("later-page network failure")
				}
				switch mode {
				case "malformed":
					return []byte(`[`), nil
				case "null":
					return []byte(`null`), nil
				case "null-entry":
					return []byte(`[null]`), nil
				case "missing-tag":
					return []byte(`[{"draft":true}]`), nil
				case "object":
					return []byte(`{"message":"not a release list"}`), nil
				case "exact-tag":
					return json.Marshal([]releaseStatus{{Tag: "v0.0.110", Draft: true}, {Tag: "other-v0.0.11", Draft: true}})
				case "absent":
					return []byte(`[]`), nil
				case "duplicate-same-page":
					return json.Marshal([]releaseStatus{match, match})
				}
				if mode != "full-bound" && mode != "oversized-page" && calls == 2 {
					return json.Marshal([]releaseStatus{match})
				}
				page := make([]releaseStatus, 100)
				for i := range page {
					page[i].Tag = fmt.Sprintf("v9.%d.%d", calls, i)
				}
				if mode == "duplicate-later-page" || mode == "later-error" || mode == "full-bound" {
					page[0] = match
				}
				if mode == "full-bound" && calls != 1 {
					page[0].Tag = fmt.Sprintf("v8.%d.0", calls)
				}
				if mode == "oversized-page" {
					page = append(page, match)
				}
				return json.Marshal(page)
			}
			s, exists, err := status(context.Background(), config{version: "0.0.11"}, run)
			switch mode {
			case "later-draft":
				if err != nil || !exists || !s.Draft || calls != 2 {
					t.Fatalf("later-page draft lost: %+v exists=%v calls=%d err=%v", s, exists, calls, err)
				}
			case "exact-tag", "absent":
				if err != nil || exists || calls != 1 {
					t.Fatalf("absence or exact-tag matching failed: exists=%v calls=%d err=%v", exists, calls, err)
				}
			default:
				if err == nil || exists {
					t.Fatalf("trusted incomplete/ambiguous discovery: exists=%v calls=%d err=%v", exists, calls, err)
				}
			}
		})
	}
}

func TestExistingEmptyDraftRetryDoesNotCreateAnotherDraft(t *testing.T) {
	built := false
	run := func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
		if name == "gh" && len(args) == 2 && args[0] == "api" && args[1] == "repos/Agenxy/dibs/releases?per_page=100&page=1" {
			return []byte(`[{"tag_name":"v0.0.11","draft":true,"immutable":false,"assets":[]}]`), nil
		}
		if name == "goreleaser" {
			built = true
			return nil, errors.New("fixture build stop")
		}
		t.Fatalf("existing empty draft retry attempted to create/mutate: %s %v", name, args)
		return nil, nil
	}
	err := publish(context.Background(), config{version: "0.0.11"}, run)
	if !built || err == nil || !strings.Contains(err.Error(), "fixture build stop") {
		t.Fatalf("did not reuse existing draft: built=%v err=%v", built, err)
	}
}

func TestReleaseListFailureNeverCreatesOrMutatesADraft(t *testing.T) {
	for _, failure := range []string{"not found (HTTP 404)", "unauthorized (HTTP 401)", "network unavailable"} {
		t.Run(failure, func(t *testing.T) {
			run := func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
				if name != "gh" || len(args) != 2 || args[0] != "api" || strings.Contains(args[1], "/tags/") {
					t.Fatalf("failed discovery attempted a mutation or wrong endpoint: %s %v", name, args)
				}
				return nil, errors.New(failure)
			}
			if err := publish(context.Background(), config{version: "0.0.11"}, run); err == nil {
				t.Fatal("API failure was treated as release absence")
			}
		})
	}
}

// Opt-in live contract measurement. It runs status() with the production
// subprocess implementation and permits only read-only list API requests.
// Ordinary CI has no external dependency and skips this measurement.
func TestReleaseDiscoveryRealAPI(t *testing.T) {
	version := os.Getenv("DIBS_TEST_RELEASE_DISCOVERY_VERSION")
	if version == "" {
		t.Skip("set DIBS_TEST_RELEASE_DISCOVERY_VERSION to measure the live API read-only")
	}
	if !versionPattern.MatchString(version) {
		t.Fatal("live probe needs a canonical version")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		if name != "gh" || len(args) != 2 || args[0] != "api" || !strings.HasPrefix(args[1], "repos/Agenxy/dibs/releases?per_page=100&page=") {
			t.Fatalf("live probe refused a non-list command: %s %v", name, args)
		}
		return command(ctx, env, name, args...)
	}
	s, exists, err := status(ctx, config{version: version}, run)
	if err != nil {
		t.Fatalf("live release discovery failed: %v", err)
	}
	if want := os.Getenv("DIBS_TEST_RELEASE_DISCOVERY_EXISTS"); want != "" && fmt.Sprint(exists) != want {
		t.Fatalf("live release exists = %v, want %s", exists, want)
	}
	if want := os.Getenv("DIBS_TEST_RELEASE_DISCOVERY_DRAFT"); want != "" {
		if !exists {
			t.Fatal("live probe usage error: DRAFT expectation requires an existing release")
		}
		if fmt.Sprint(s.Draft) != want {
			t.Fatalf("live draft state = %v, want %s", s.Draft, want)
		}
	}
	t.Logf("real production-door API: tag=v%s exists=%v draft=%v immutable=%v assets=%d",
		version, exists, s.Draft, s.Immutable, len(s.Assets))
}
