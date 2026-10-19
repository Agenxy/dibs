// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"fmt"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// Coordination must not excuse a separate shared objective or a reference
// whose real participants are somebody else. Tracker cases carry confirmed
// repository evidence; unknown identities get a separate quiet suggestion.
func TestCoordinationDoesNotHideUncoveredWorkThroughDeclareMCP(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	g := &gitFixtures{t: t, git: git, root: t.TempDir()}
	repo := g.repo("uncovered-work", "https://github.com/fixture/uncovered-work.git")

	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, scenario := range []string{"mixed-objective", "one-sided-wait", "third-party", "wrong-kind"} {
			t.Run(version+"/"+scenario, func(t *testing.T) {
				srv, _, _ := aliasReplayServer(t, t.TempDir())
				lead := boundaryRegisterAt(t, srv, version, "lead", repo)
				worker := boundaryRegisterAt(t, srv, version, "worker", repo)
				owner := boundaryRegisterAt(t, srv, version, "owner", repo)
				sender := lead
				if scenario == "one-sided-wait" || scenario == "third-party" {
					sender = owner
				}
				sent := toolCallOn(t, srv, version, "send", map[string]any{
					"token": sender, "to": "worker", "type": "request", "body": "bounded coordination",
				})
				serial, _ := sent["msg_serial"].(float64)
				if serial == 0 {
					t.Fatalf("setup request: %v", sent)
				}
				kind := "request"
				if scenario == "wrong-kind" {
					kind = "question"
				}
				refs := []string{fmt.Sprintf("%s:%.0f", kind, serial)}
				if scenario == "mixed-objective" {
					refs = append(refs, "issue:42")
				}
				for i, token := range []string{lead, worker} {
					args := map[string]any{"token": token, "text": "implement work", "activity": "implement", "refs": refs}
					if scenario == "mixed-objective" || (scenario == "one-sided-wait" && i == 0) {
						args["waiting"] = "owner"
					}
					r := toolCallOn(t, srv, version, "declare", args)
					if r["ok"] != true || (i == 1 && r["warning"] == nil) {
						t.Fatalf("uncovered objective lost its warning: %v", r)
					}
				}
			})
		}
	}
}

// A proven mail relationship cannot swallow a separate, unconfirmed tracker
// number. Unknown provenance remains visible without becoming a warning.
func TestCoordinationKeepsUnconfirmedTrackerRefVisibleThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, _ := aliasReplayServer(t, t.TempDir())
			lead := boundaryRegisterAt(t, srv, version, "lead", "")
			worker := boundaryRegisterAt(t, srv, version, "worker", "")
			sent := toolCallOn(t, srv, version, "send", map[string]any{"token": lead, "to": "worker", "type": "request", "body": "shared process"})
			serial, ok := sent["msg_serial"].(float64)
			if !ok || serial == 0 {
				t.Fatalf("setup request: %v", sent)
			}
			refs := []string{fmt.Sprintf("request:%.0f", serial), "issue:42"}
			for i, token := range []string{lead, worker} {
				r := toolCallOn(t, srv, version, "declare", map[string]any{"token": token, "text": "implement work", "activity": "implement", "refs": refs, "waiting": "owner"})
				if r["ok"] != true {
					t.Fatalf("setup declare: %v", r)
				}
				if i == 0 {
					continue
				}
				rows, _ := r["overlaps"].([]any)
				found := false
				for _, value := range rows {
					row, _ := value.(map[string]any)
					found = found || row["signal"] == "repository-unconfirmed"
				}
				note, _ := r["note"].(string)
				if !found || !strings.Contains(note, "repository unconfirmed") || r["warning"] != nil {
					t.Fatalf("coordination hid or promoted the unconfirmed tracker ref: %v", r)
				}
			}
		})
	}
}

func boundaryRegisterAt(t *testing.T, srv *httptest.Server, version, name, cwd string) string {
	t.Helper()
	r := toolCallOn(t, srv, version, "register", map[string]any{"name": name, "nonce": "boundary-" + name, "cwd": cwd})
	token, _ := r["token"].(string)
	if token == "" {
		t.Fatalf("setup registration: %v", r)
	}
	if c := toolCallOn(t, srv, version, "check_in", map[string]any{"token": token}); c["ok"] != true {
		t.Fatalf("setup check_in: %v", c)
	}
	return token
}
