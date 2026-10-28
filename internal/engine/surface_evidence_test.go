// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestSurfaceEvidenceDoesNotRefreshRecencyOrConfirmSocketMail(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	old := time.Now().Add(-2 * time.Hour).UTC()
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpRegister, Name: "worker", NewToken: "worker-token", Description: "keep",
		SessionID: "01a0696b-8446-7821-a992-9dc7f6a43a25",
		Agent:     &core.AgentInfo{Harness: "Codex", Model: "keep-model", HostID: "test"},
	}, old); err != nil {
		t.Fatal(err)
	}
	e := New(st, &memLedger{}, nil)
	e.SetWakeCommands(map[string]WakeCommand{
		"codex": {Argv: []string{"/bin/echo", "{message}"}, Cooldown: time.Minute},
	})
	l := st.Agents["worker"]
	l.Status = core.StatusDormant
	e.seen[l.ID] = old
	e.contact = map[string]contactEvidence{l.ID: {at: old, created: l.CreatedSerial}}
	e.socketOffers = map[string]socketOffer{l.ID: {id: "keep-offer", session: l.SessionID, at: old, written: true, canConfirm: true}}
	e.socketTurns = map[string]socketTurn{socketSessionKey(l): {state: "idle", at: old}}
	if e.recentlyInTouch(l) {
		t.Fatal("setup: old dormant row already reads recently in touch")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpUpdate, Token: l.Token, KeepDescription: true,
		Agent: &core.AgentInfo{Surface: "chatgpt-app"}, BridgeSurfaceEvidence: true, BridgeSurfaceTrusted: true,
	})
	if err != nil || res["ok"] != true {
		t.Fatalf("surface evidence refused: %v %v", res, err)
	}
	observed, err := e.query(ctx, func() core.Result {
		row := e.state.Agents["worker"]
		return core.Result{
			"recent": e.recentlyInTouch(row), "seen": e.seen[row.ID], "contact": e.contact[row.ID].at,
			"offer": e.socketOffers[row.ID].id, "lifecycle": e.socketTurns[socketSessionKey(row)].state,
			"coordination": row.LastCoordination, "status": row.Status,
			"surface": row.Agent.Surface, "model": row.Agent.Model, "description": row.Description,
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed["recent"] != false || observed["seen"] != old || observed["contact"] != old ||
		observed["coordination"] != old || observed["offer"] != "keep-offer" || observed["lifecycle"] != "idle" ||
		observed["status"] != core.StatusDormant {
		t.Fatalf("background evidence changed recency, lifecycle, or socket acceptance: %v", observed)
	}
	if observed["surface"] != "chatgpt-app" || observed["model"] != "keep-model" || observed["description"] != "keep" {
		t.Fatalf("background update lost surface or identity: %v", observed)
	}
}
