// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/overlap"
)

// The two reported relationships enter through real send/declare operations,
// not fabricated slots. Equal implement roles prevent the existing role rule
// from concealing either regression.
func TestStructuralCoordinationThroughDeclareMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		for _, scenario := range []string{"requester-doer", "question-parties", "shared-wait"} {
			t.Run(version+"/"+scenario, func(t *testing.T) {
				limits := core.DefaultLimits()
				limits.TerminalRetention = 1
				srv, eng, _ := aliasReplayServer(t, t.TempDir(), limits)
				_, lead := aliasRegister(t, srv, "lead", "coordination-lead")
				_, worker := aliasRegister(t, srv, "worker", "coordination-worker")
				kind, sender, target := "request", lead, "worker"
				if scenario != "requester-doer" {
					kind = "question"
				}
				if scenario == "shared-wait" {
					_, sender = aliasRegister(t, srv, "owner", "coordination-owner")
					target = "lead"
				}
				sent := toolCallOn(t, srv, version, "send", map[string]any{
					"token": sender, "to": target, "type": kind, "body": "one shared coordination fact",
				})
				serial, ok := sent["msg_serial"].(float64)
				if !ok || serial == 0 {
					t.Fatalf("setup mail: %v", sent)
				}
				ref := fmt.Sprintf("%s:%.0f", kind, serial)
				args := map[string]any{"token": lead, "text": "one work item", "activity": "implement", "refs": []string{ref}}
				if scenario == "shared-wait" {
					args["waiting"] = "owner"
				}
				first := toolCallOn(t, srv, version, "declare", args)
				if first["slot_id"] == nil {
					t.Fatalf("setup first declaration: %v", first)
				}
				args["token"] = worker
				r := toolCallOn(t, srv, version, "declare", args)
				assertCoordinationOverlap(t, r)
				if scenario != "shared-wait" {
					return
				}
				// Shared waiting is a structural declaration fact, not mail-body
				// inference, and must survive the question's live retention.
				answered := toolCallOn(t, srv, version, "respond", map[string]any{
					"token": lead, "msg_serial": serial, "disposition": "answer", "body": "decision recorded",
				})
				if answered["state"] != "answered" {
					t.Fatalf("setup answer: %v", answered)
				}
				pressure := toolCallOn(t, srv, version, "send", map[string]any{
					"token": sender, "to": "lead", "type": "notify", "body": "newer retained mail",
				})
				if pressure["msg_serial"] == nil {
					t.Fatalf("setup retention pressure: %v", pressure)
				}
				acked := toolCallOn(t, srv, version, "ack", map[string]any{"token": lead, "msg_serial": pressure["msg_serial"]})
				if acked["ok"] != true {
					t.Fatalf("setup acknowledgement: %v", acked)
				}
				if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpSweep, PurgeMail: true}); err != nil {
					t.Fatal(err)
				}
				gone := toolCallOn(t, srv, version, "read_mail", map[string]any{"token": sender, "msg_serial": serial})
				if gone["code"] != "E_NO_MESSAGE" {
					t.Fatalf("setup question not retired: %v", gone)
				}
				args["slot_id"] = r["slot_id"]
				assertCoordinationOverlap(t, toolCallOn(t, srv, version, "declare", args))
			})
		}
	}
}

// The incoming declaration must carry waiting into matching before it is
// applied. Calling EvidenceBetween by hand would miss that ingress wire.
func TestSharedWaitReachesMatchingThroughDeclareMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			repo := t.TempDir()
			srv, eng, _ := aliasReplayServer(t, t.TempDir())
			cfg := engine.DefaultMatchConfig
			cfg.Repo, cfg.AutoJoin = repo, "never"
			eng.SetScorerForRepo(repo, overlap.NewLexicalFromFiles([]string{"coordination.go"}, nil), cfg)
			tokens := make([]string, 0, 3)
			for _, name := range []string{"owner", "lead", "worker"} {
				r := toolCallOn(t, srv, version, "register", map[string]any{
					"name": name, "nonce": "matching-" + name, "cwd": repo,
				})
				token, _ := r["token"].(string)
				if token == "" {
					t.Fatalf("setup registration: %v", r)
				}
				if check := toolCallOn(t, srv, version, "check_in", map[string]any{"token": token}); check["ok"] != true {
					t.Fatalf("setup check in: %v", check)
				}
				tokens = append(tokens, token)
			}
			sent := toolCallOn(t, srv, version, "send", map[string]any{
				"token": tokens[0], "to": "lead", "type": "question", "body": "shared owner decision",
			})
			serial, _ := sent["msg_serial"].(float64)
			if serial == 0 {
				t.Fatalf("setup question: %v", sent)
			}
			for i, token := range tokens[1:] {
				r := toolCallOn(t, srv, version, "declare", map[string]any{
					"token": token, "text": "coordination.go", "activity": "implement", "waiting": "owner",
					"refs": []string{fmt.Sprintf("question:%.0f", serial)}, "dirs": []string{"coordination.go"},
				})
				spaces, _ := r["spaces"].([]any)
				if r["ok"] != true || len(spaces) != 1 {
					t.Fatalf("setup matching path: %v", r)
				}
				if i == 0 {
					continue
				}
				evidence, _ := spaces[0].(map[string]any)["evidence"].(map[string]any)
				if evidence["complementary"] != true {
					t.Errorf("shared waiting lost before matching: %v", spaces[0])
				}
			}
		})
	}
}

func assertCoordinationOverlap(t *testing.T, r map[string]any) {
	t.Helper()
	if r["ok"] != true || r["warning"] != nil {
		t.Errorf("coordination falsely warns of duplication: %v", r)
	}
	overlaps, _ := r["overlaps"].([]any)
	if len(overlaps) != 1 {
		t.Fatalf("missing peer relationship: %v", r)
	}
	o := overlaps[0].(map[string]any)
	if o["signal"] != "shared-coordination" || o["complementary"] != true {
		t.Errorf("structural relationship still reported as same objective: %v", o)
	}
}

func TestUnrelatedSharedWorkStillWarnsThroughDeclareMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, _ := aliasReplayServer(t, t.TempDir())
			_, a := aliasRegister(t, srv, "one", "duplicate-one")
			_, b := aliasRegister(t, srv, "two", "duplicate-two")
			for _, token := range []string{a, b} {
				r := toolCallOn(t, srv, version, "declare", map[string]any{
					"token": token, "text": "implement same ticket", "activity": "implement", "refs": []string{"issue:42"},
				})
				if r["ok"] != true || (token == b && r["warning"] == nil) {
					t.Fatalf("genuine duplication lost its warning: %v", r)
				}
			}
		})
	}
}
