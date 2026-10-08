// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func immutableStatusJSON(s releaseStatus, value any, missing bool) ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err = json.Unmarshal(b, &object); err != nil {
		return nil, err
	}
	if missing {
		delete(object, "immutable")
	} else {
		object["immutable"] = value
	}
	return json.Marshal([]map[string]any{object})
}

// Enter through the real publisher, not a setter or a stand-alone flag check.
// These are process-door/ordering proofs, not a substitute for live GitHub or
// cryptographic verification. The existing cosign policy is unchanged.
func TestPublishedReleaseRequiresImmutableReadback(t *testing.T) {
	for _, mode := range []string{"immutable", "mutable", "missing", "null", "wrong-type", "still-draft", "status-unavailable", "ambiguous-edit", "public-tamper"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t)
			useCosignFixture(t)
			if err := localTag(context.Background(), c, command); err != nil {
				t.Fatal(err)
			}
			git(t, "push", "origin", "refs/tags/v0.0.11")
			s := fixtureAssets(t, c, "dist")
			s.Draft = true
			c.publicationRun = "456"
			edited, finalRead, publicReadback := false, false, false
			run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
				if name == "git" {
					return command(ctx, env, name, args...)
				}
				if name == "gh" && args[0] == "api" {
					if !edited {
						return immutableStatusJSON(s, false, false)
					}
					finalRead = true
					if mode == "status-unavailable" {
						return nil, errors.New("GitHub read unavailable")
					}
					s.Draft = mode == "still-draft"
					var value any = mode == "immutable" || mode == "ambiguous-edit" || mode == "public-tamper"
					if mode == "null" {
						value = nil
					}
					if mode == "wrong-type" {
						value = "true"
					}
					return immutableStatusJSON(s, value, mode == "missing")
				}
				if name == "gh" && args[0] == "release" {
					switch args[1] {
					case "upload":
						return nil, nil
					case "download":
						copyFixture(t, "dist", args[6], c)
						publicReadback = publicReadback || edited
						if edited && mode == "public-tamper" {
							write(t, filepath.Join(args[6], "dibs.rb"), "changed before publication")
						}
						return nil, nil
					case "edit":
						edited = true
						if mode == "ambiguous-edit" {
							return nil, errors.New("edit response lost after apply")
						}
						return nil, nil
					}
				}
				t.Fatalf("unexpected command %s %v", name, args)
				return nil, nil
			}
			err := publishDraft(context.Background(), c, run)
			good := mode == "immutable" || mode == "ambiguous-edit"
			if !edited || !finalRead || (err == nil) != good || publicReadback != (good || mode == "public-tamper") {
				t.Fatalf("mode %s: err=%v edited=%v final-read=%v public-readback=%v", mode, err, edited, finalRead, publicReadback)
			}
			if !good && mode != "public-tamper" {
				settingHint := strings.Contains(err.Error(), "published release reports immutable:false")
				wantSettingHint := mode == "mutable" || mode == "missing" || mode == "null"
				if settingHint != wantSettingHint {
					t.Fatalf("wrong repository-setting hint for %s: %v", mode, err)
				}
				if mode == "still-draft" && (!strings.Contains(err.Error(), "still a draft") ||
					!strings.Contains(err.Error(), "full_publication_run=456")) {
					t.Fatalf("missing still-draft recovery command: %v", err)
				}
			}
		})
	}
}

func TestUploadRefusalOnlyAcceptsEquivalentImmutablePublicAssets(t *testing.T) {
	for _, mode := range []string{"equivalent", "tampered", "different-signed-bytes", "missing-asset", "mutable", "still-draft", "status-unavailable", "bad-tag"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t)
			useCosignFixture(t)
			if err := localTag(context.Background(), c, command); err != nil {
				t.Fatal(err)
			}
			git(t, "push", "origin", "refs/tags/v0.0.11")
			s := fixtureAssets(t, c, "dist")
			s.Draft = true
			uploads, downloads, edits := 0, 0, 0
			run := func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
				if name == "git" {
					return command(ctx, env, name, args...)
				}
				if name == "gh" && args[0] == "api" {
					if uploads == 0 {
						return immutableStatusJSON(s, false, false)
					}
					if mode == "status-unavailable" {
						return nil, errors.New("GitHub read unavailable")
					}
					s.Draft = mode == "still-draft"
					if mode == "missing-asset" {
						s.Assets = s.Assets[1:]
					}
					return immutableStatusJSON(s, mode != "mutable", false)
				}
				if name == "gh" && args[0] == "release" {
					switch args[1] {
					case "upload":
						uploads++
						if mode == "bad-tag" {
							git(t, "push", "origin", ":refs/tags/v0.0.11")
						}
						return nil, errors.New("upload refused by immutable public release (HTTP 422)")
					case "download":
						downloads++
						copyFixture(t, "dist", args[6], c)
						if mode == "tampered" {
							write(t, filepath.Join(args[6], "dibs.rb"), "different public bytes")
						}
						if mode == "different-signed-bytes" {
							write(t, filepath.Join(args[6], "dibs.rb"), "different signed public bytes")
							write(t, filepath.Join(args[6], "checksums.txt"), "")
							if err := completeChecksums(c, args[6]); err != nil {
								t.Fatal(err)
							}
						}
						return nil, nil
					case "edit":
						edits++
					}
				}
				t.Fatalf("unexpected mutation/command %s %v", name, args)
				return nil, nil
			}
			err := publishDraft(context.Background(), c, run)
			if uploads != 1 || edits != 0 || (err == nil) != (mode == "equivalent") {
				t.Fatalf("mode %s: err=%v uploads=%d downloads=%d edits=%d", mode, err, uploads, downloads, edits)
			}
			if mode == "equivalent" && downloads != 1 {
				t.Fatal("accepted upload refusal without public byte verification")
			}
		})
	}
}
