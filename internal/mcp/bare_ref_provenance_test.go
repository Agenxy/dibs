// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/overlap"
)

// Exercise registration's real Git resolver, declaration warnings, and the
// separate space-matching projection. The unknown verdict is intentionally
// quieter than the old unknown-as-duplication rule (architect 188961).
func TestBareRefRepositoryVerdictsThroughMCP(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	g := &gitFixtures{t: t, git: git, root: t.TempDir()}
	one := g.repo("one", "https://github.com/fixture/one.git")
	two := g.repo("two", "https://github.com/fixture/two.git")
	unknown := t.TempDir()
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, ref := range []string{"pr:8", "issue:8"} {
			for _, verdict := range []string{"same", "different", "unknown"} {
				t.Run(version+"/"+ref+"/"+verdict, func(t *testing.T) {
					peer := map[string]string{"same": one, "different": two, "unknown": unknown}[verdict]
					srv, eng, _ := aliasReplayServer(t, t.TempDir())
					cfg := engine.DefaultMatchConfig
					cfg.AutoJoin = engine.AutoJoinAlways
					cfg.NotifyThreshold = 0.9
					eng.SetScorerForRepo(one, overlap.NewLexicalFromFiles(nil, nil), cfg)
					eng.SetScorerForRepo(peer, overlap.NewLexicalFromFiles(nil, nil), cfg)
					tokens := make([]string, 0, 2)
					for i, cwd := range []string{one, peer} {
						name := []string{"first", "second"}[i]
						r := toolCallOn(t, srv, version, "register", map[string]any{"name": name, "nonce": "bare-ref-" + name, "cwd": cwd})
						tok, _ := r["token"].(string)
						if tok == "" {
							t.Fatalf("setup register: %v", r)
						}
						if check := toolCallOn(t, srv, version, "check_in", map[string]any{"token": tok}); check["ok"] != true {
							t.Fatalf("setup check_in: %v", check)
						}
						tokens = append(tokens, tok)
					}
					declare := func(token string) map[string]any {
						r := toolCallOn(t, srv, version, "declare", map[string]any{"token": token, "text": "work on the declared ticket", "activity": "implement", "refs": []string{ref}})
						if r["ok"] != true {
							t.Fatalf("setup declare: %v", r)
						}
						return r
					}
					first := declare(tokens[0])
					if first["spaces"] == nil {
						t.Fatalf("setup matching path absent: %v", first)
					}
					r := declare(tokens[1])
					assertBareRefVerdict(t, r, verdict)
				})
			}
		}
	}
}

func assertBareRefVerdict(t *testing.T, r map[string]any, verdict string) {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	switch verdict {
	case "same":
		if r["warning"] == nil || !strings.Contains(body, `"relation":"same_work_item"`) {
			t.Errorf("confirmed repository lost objective identity: %s", body)
		}
	case "different":
		if r["warning"] != nil || strings.Contains(body, `"shared_refs"`) || strings.Contains(body, `"same-objective"`) || strings.Contains(body, `"identity"`) {
			t.Errorf("different repositories share only a number: %s", body)
		}
	case "unknown":
		peerSuggestion := false
		spaces, _ := r["spaces"].([]any)
		for _, value := range spaces {
			row, _ := value.(map[string]any)
			evidence, _ := row["evidence"].(map[string]any)
			refs, _ := evidence["unconfirmed_refs"].([]any)
			if len(refs) > 0 {
				peerSuggestion = true
				if evidence["identity"] != nil || row["shared_refs"] != nil {
					t.Errorf("unconfirmed number gained identity authority: %v", row)
				}
			}
		}
		if !peerSuggestion {
			t.Errorf("unknown ref disappeared from the space projection: %s", body)
		}
		if r["warning"] != nil || strings.Contains(body, `"relation":"same_work_item"`) || strings.Contains(body, `"action":"joined"`) || !strings.Contains(body, "repository unconfirmed") {
			t.Errorf("unknown repository must be a quiet unconfirmed suggestion: %s", body)
		}
	}
}

// Board mail serials remain global even when their parties work in different
// repositories. Applying one repository gate to every namespace loses routing.
func TestGlobalCoordinationRefAcrossRepositoriesThroughMCP(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	g := &gitFixtures{t: t, git: git, root: t.TempDir()}
	repos := []string{g.repo("requester", "https://github.com/fixture/requester.git"), g.repo("doer", "https://github.com/fixture/doer.git")}
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, kind := range []string{"request", "question"} {
			t.Run(version+"/"+kind, func(t *testing.T) {
				srv, _, _ := aliasReplayServer(t, t.TempDir())
				var tokens []string
				for i, name := range []string{"lead", "worker"} {
					r := toolCallOn(t, srv, version, "register", map[string]any{"name": name, "nonce": "global-ref-" + name, "cwd": repos[i]})
					token, _ := r["token"].(string)
					if token == "" {
						t.Fatalf("setup register: %v", r)
					}
					if c := toolCallOn(t, srv, version, "check_in", map[string]any{"token": token}); c["ok"] != true {
						t.Fatalf("setup check_in: %v", c)
					}
					tokens = append(tokens, token)
				}
				sent := toolCallOn(t, srv, version, "send", map[string]any{"token": tokens[0], "to": "worker", "type": kind, "body": "one board-wide item"})
				serial, ok := sent["msg_serial"].(float64)
				if !ok || serial == 0 {
					t.Fatalf("setup mail: %v", sent)
				}
				ref := fmt.Sprintf("%s:%.0f", kind, serial)
				for i, token := range tokens {
					r := toolCallOn(t, srv, version, "declare", map[string]any{"token": token, "text": "coordinate the board item", "activity": "implement", "refs": []string{ref}})
					if r["ok"] != true {
						t.Fatalf("setup declare: %v", r)
					}
					if i == 1 {
						assertCoordinationOverlap(t, r)
					}
				}
			})
		}
	}
}
