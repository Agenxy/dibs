// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The tutorial prints selected response fields, not a fabricated whole reply.
// Produce those fields through the same register/check_in/declare tools readers
// use, with real Git discovery for the confirmed case.
type tutorialOverlapResponse struct {
	OK       bool                      `json:"ok"`
	Overlaps []tutorialOverlapEvidence `json:"overlaps"`
	Warning  string                    `json:"warning,omitempty"`
	Note     string                    `json:"note,omitempty"`
}

type tutorialOverlapEvidence struct {
	Agent  string `json:"agent"`
	Signal string `json:"signal"`
	Kind   string `json:"kind"`
}

func TestTutorialOverlapExamplesMatchTheRealTools(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("tutorial's confirmed-repository fixture requires git:", err)
	}
	g := &gitFixtures{t: t, git: git, root: t.TempDir()}
	repo := g.repo("tutorial", "https://github.com/fixture/tutorial.git")
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, kind := range []string{"confirmed", "no-repository", "no-cwd"} {
			t.Run(version+"/"+kind, func(t *testing.T) {
				cwd, example := repo, "confirmed"
				switch kind {
				case "no-repository":
					cwd, example = t.TempDir(), "unconfirmed"
				case "no-cwd":
					cwd, example = "", "unconfirmed"
				}
				srv, _ := newServer(t)
				for i, name := range []string{"refactor-bot", "second-bot"} {
					token := boundaryRegisterAt(t, srv, version, name, cwd)
					text := "Reworking how the session store handles reconnects"
					if i == 1 {
						text = "Fixing session reconnect handling"
					}
					reply := toolCallOn(t, srv, version, "declare", map[string]any{
						"token": token, "text": text, "dirs": []string{"internal/session"}, "refs": []string{"issue:1140"},
					})
					if reply["ok"] != true || reply["slot_id"] == nil {
						t.Fatalf("setup: declaration failed: %v", reply)
					}
					if i == 0 {
						continue
					}
					data, err := json.Marshal(reply)
					if err != nil {
						t.Fatal(err)
					}
					var actual tutorialOverlapResponse
					if err := json.Unmarshal(data, &actual); err != nil {
						t.Fatal(err)
					}
					if len(actual.Overlaps) == 0 || (example == "confirmed") != (actual.Warning != "") {
						t.Fatalf("setup: repository evidence did not reach the declared overlap: %s", data)
					}
					shown, err := json.MarshalIndent(actual, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					want := tutorialOverlapExcerpt(t, example, shown)
					if !reflect.DeepEqual(actual, want) {
						t.Fatalf("tutorial %s differs from real %s tools; replace excerpt with:\n%s", example, version, shown)
					}
				}
			})
		}
	}
}

func tutorialOverlapExcerpt(t *testing.T, kind string, actual []byte) tutorialOverlapResponse {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "TUTORIAL.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, tail, found := strings.Cut(string(doc), "<!-- tutorial-overlap:"+kind+" -->")
	if !found {
		t.Fatalf("tutorial has no checked %s excerpt; real response fields:\n%s", kind, actual)
	}
	_, tail, found = strings.Cut(tail, "```json\n")
	if !found {
		t.Fatal("tutorial excerpt has no JSON fence")
	}
	body, _, found := strings.Cut(tail, "\n```")
	if !found {
		t.Fatal("tutorial excerpt has no closing fence")
	}
	var shown tutorialOverlapResponse
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&shown); err != nil {
		t.Fatal("tutorial excerpt is not JSON:", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		t.Fatal("tutorial excerpt has trailing JSON")
	}
	return shown
}
