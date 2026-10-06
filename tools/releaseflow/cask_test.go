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

func TestCaskRetriesVerifyEquivalenceBeforeAnyPush(t *testing.T) {
	for _, mode := range []string{
		"main-matches", "branch-matches", "branch-differs", "key-missing", "invalid-key", "new-branch", "mutable-public",
	} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t)
			useCosignFixture(t)
			stage := t.TempDir()
			s := fixtureAssets(t, c, stage)
			if mode == "mutable-public" {
				s.Immutable = false
			}
			t.Setenv("HOMEBREW_TAP_DEPLOY_KEY", "")
			if mode == "new-branch" || mode == "invalid-key" {
				t.Setenv("HOMEBREW_TAP_DEPLOY_KEY", "fixture secret, not logged\r\n")
			}
			var calls []string
			pushed := false
			validated := false
			clonePath := ""
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
					clonePath = args[len(args)-1]
					data := "old cask"
					if mode == "main-matches" {
						data = "fixture dibs.rb"
					}
					write(t, filepath.Join(args[len(args)-1], "Casks", "dibs.rb"), data)
					return nil, nil
				}
				if name == "ssh-keygen" && args[0] == "-y" {
					data, err := os.ReadFile(args[len(args)-1])
					if err != nil || string(data) != "fixture secret, not logged\n" {
						t.Fatalf("deploy key was not normalized before validation: %v", err)
					}
					if mode == "invalid-key" {
						return nil, errors.New("fixture invalid key")
					}
					validated = true
					return []byte("fixture public key"), nil
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
						data, err := os.ReadFile(filepath.Join(filepath.Dir(clonePath), "deploy-key"))
						if err != nil || string(data) != "fixture secret, not logged\n" || !validated {
							t.Fatalf("push preceded deploy-key normalization/validation: %v", err)
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

func TestNormalizedCaskKey(t *testing.T) {
	for _, input := range []string{"line", "line\n", "line\r\n", "line\r\n\r\n"} {
		if got := normalizedCaskKey(input); got != "line\n" {
			t.Fatalf("normalization of %q = %q", input, got)
		}
	}
}

func TestKeyDiagnosticOnlyChecksPrivateTempFiles(t *testing.T) {
	c := fixture(t)
	c.phase = "cask-key-diagnose"
	t.Setenv("HOMEBREW_TAP_DEPLOY_KEY", "fixture secret\r\n")
	var checked []string
	run := func(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
		if name != "ssh-keygen" || strings.Join(args[:4], " ") != "-y -P  -f" {
			t.Fatalf("diagnostic attempted more than key parsing: %s %v", name, args)
		}
		data, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			t.Fatal(err)
		}
		checked = append(checked, string(data))
		if strings.Contains(string(data), "\r") {
			return nil, errors.New("raw key invalid")
		}
		return []byte("derived public key must not be logged"), nil
	}
	if err := execute(context.Background(), c, run); err != nil {
		t.Fatal(err)
	}
	if strings.Join(checked, "|") != "fixture secret\r\n|fixture secret\n" {
		t.Fatalf("diagnostic did not compare raw and normalized formats: %q", checked)
	}
}
