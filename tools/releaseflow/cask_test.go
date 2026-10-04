package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaskRetriesVerifyEquivalenceBeforeAnyPush(t *testing.T) {
	for _, mode := range []string{"main-matches", "branch-matches", "branch-differs", "key-missing", "new-branch", "mutable-public"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t)
			useCosignFixture(t)
			stage := t.TempDir()
			s := fixtureAssets(t, c, stage)
			if mode == "mutable-public" {
				s.Immutable = false
			}
			t.Setenv("HOMEBREW_TAP_DEPLOY_KEY", "")
			if mode == "new-branch" {
				t.Setenv("HOMEBREW_TAP_DEPLOY_KEY", "fixture secret, not logged")
			}
			var calls []string
			pushed := false
			run := func(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				calls = append(calls, name+" "+joined)
				if name == "gh" && args[0] == "api" {
					return json.Marshal([]releaseStatus{s})
				}
				if name == "gh" && args[0] == "release" && args[1] == "download" {
					copyFixture(t, stage, args[6], c)
					return nil, nil
				}
				if name == "git" && args[0] == "clone" {
					data := "old cask"
					if mode == "main-matches" {
						data = "fixture dibs.rb"
					}
					write(t, filepath.Join(args[len(args)-1], "Casks", "dibs.rb"), data)
					return nil, nil
				}
				if name == "git" && len(args) > 2 && args[0] == "-C" {
					switch args[2] {
					case "ls-remote":
						if strings.HasPrefix(mode, "branch-") {
							return []byte(strings.Repeat("a", 40) + "\trefs/heads/cask-0.0.11\n"), nil
						}
						return nil, nil
					case "fetch":
						return nil, nil
					case "show":
						if mode == "branch-matches" {
							return []byte("fixture dibs.rb"), nil
						}
						return []byte("different"), nil
					case "switch", "add", "-c":
						if mode != "new-branch" {
							t.Fatal("mutated tap before equivalence/key proof")
						}
						return nil, nil
					case "push":
						if mode != "new-branch" || args[len(args)-1] != "HEAD:refs/heads/cask-0.0.11" || len(env) != 1 || !strings.Contains(env[0], "StrictHostKeyChecking=yes") {
							t.Fatalf("wrong push: %v %v", env, args)
						}
						pushed = true
						return nil, nil
					}
				}
				return nil, errors.New("unexpected: " + name + " " + joined)
			}
			err := publishCask(context.Background(), c, run)
			good := mode == "main-matches" || mode == "branch-matches" || mode == "new-branch"
			if (err == nil) != good || pushed != (mode == "new-branch") {
				t.Fatalf("%s: %v push%v calls%v", mode, err, pushed, calls)
			}
		})
	}
}
