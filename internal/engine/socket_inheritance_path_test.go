// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Drive real registration/update ingress and deterministic sweep ops, never
// manufacture a guessed binding. Explicit clocks let the harness stay alive
// and renew its announcement across the default seven-day archive retention.
func TestSocketInheritanceAfterSweepKeepsGuessedProvenance(t *testing.T) {
	for _, path := range []string{"register", "update", "check_in"} {
		t.Run(path, func(t *testing.T) { socketInheritancePath(t, path) })
	}
}

func socketInheritancePath(t *testing.T, path string) {
	t.Helper()
	sock, sid := listeningSession(t)
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	now := t0Engine()
	do := func(op *core.Op, at time.Time) core.Result {
		t.Helper()
		r, err := e.exec(op, at)
		if err != nil {
			t.Fatalf("setup %s: %v", op.Kind, err)
		}
		return r
	}
	announce := func(at time.Time) {
		t.Helper()
		// The clocked handler the harness's hook/session entry calls.
		if r := e.noteChild(Child{SessionID: sid, CWD: "/w", Host: "t", State: "running"}, at); r["ok"] != true {
			t.Fatalf("setup: harness announcement failed: %v", r)
		}
	}
	register := func(name, session string, kind core.AgentKind, at time.Time) core.Result {
		return do(&core.Op{
			Kind: core.OpRegister, Name: name, Nonce: "socket-inheritance-" + name,
			SessionID: session, AgentKind: kind,
			Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/w", HostID: "t"},
		}, at)
	}
	register("original", sid, core.KindEphemeral, now)
	if a := st.Agents["original"]; a == nil || !a.HoldsSession(sid) || a.GuessedSession(sid) {
		t.Fatal("setup: original did not state its live session")
	}
	staleAt := now.Add(time.Hour)
	do(&core.Op{Kind: core.OpSweep, StaleAgents: []string{"original"}, KeepArchivedNonce: true}, staleAt)
	archivedAt := staleAt.Add(st.Limits.StaleGrace + time.Second)
	do(&core.Op{Kind: core.OpSweep, KeepArchivedNonce: true}, archivedAt)
	if st.Agents["original"].Status != core.StatusArchived || st.AgentBySession(sid) != nil {
		t.Fatal("setup: sweep did not archive the original while freeing ordinary session lookup")
	}
	announce(archivedAt.Add(time.Second))
	register("while-retained", "host-retained", core.KindPersistent, archivedAt.Add(2*time.Second))
	if st.Agents["while-retained"].HoldsSession(sid) {
		t.Error("a newcomer inherited the live session while the original archived row still owns it")
	}
	// Once retention really removes that row, the still-live harness renews
	// its announcement. Directory inference can now fire, but is only a guess.
	purgedAt := archivedAt.Add(st.Limits.ArchiveRetention + time.Second)
	do(&core.Op{Kind: core.OpSweep, PurgeMail: true, KeepArchivedNonce: true, KeepOwed: true, V7Semantics: true}, purgedAt)
	if st.Agents["original"] != nil || st.SessionSpokenFor(sid) {
		t.Fatal("setup: original session is still spoken for after retention")
	}
	var r core.Result
	if path != "register" {
		r = register("inheritor", "host-inheritor", core.KindPersistent, purgedAt)
		if st.Agents["inheritor"].HoldsSession(sid) {
			t.Fatal("setup: stale announcement was still adopted")
		}
	}
	announce(purgedAt.Add(time.Second))
	if path == "register" {
		r = register("inheritor", "host-inheritor", core.KindPersistent, purgedAt.Add(2*time.Second))
	} else {
		token, _ := r["token"].(string)
		if token == "" {
			t.Fatal("setup: register returned no token")
		}
		if path == "update" {
			do(&core.Op{
				Kind: core.OpUpdate, Token: token,
				Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/w", HostID: "t"},
			}, purgedAt.Add(2*time.Second))
		} else {
			do(&core.Op{Kind: core.OpAckBoard, Token: token, MailboxPage: true}, purgedAt.Add(2*time.Second))
		}
	}
	b := st.Agents["inheritor"]
	if b == nil || !b.HoldsSession(sid) {
		t.Fatal("setup: fresh announcement did not produce an inherited binding")
	}
	if !b.GuessedSession(sid) {
		t.Error("directory inheritance was classified as stated without the harness stating it")
	}
	token, _ := r["token"].(string)
	if token == "" {
		t.Fatal("setup: register returned no token")
	}
	do(&core.Op{
		Kind: core.OpUpdate, Token: token,
		Agent: &core.AgentInfo{Harness: "Claude Code", CWD: "/work/another-session", HostID: "t"},
	}, purgedAt.Add(3*time.Second))
	if !b.HoldsSession(sid) {
		t.Fatal("setup: update discarded the inherited binding")
	}
	if !b.GuessedSession(sid) {
		t.Error("update promoted directory inheritance to a stated binding")
	}
	e.primePeerSessions()
	plan, ok := e.wakeFor(b, core.MsgQuestion, questionFor(b.ID))
	if !ok || len(plan.argv) != 0 {
		t.Fatal("setup: inherited live session did not produce a socket plan")
	}
	wire := make(chan string, 1)
	go func() { wire <- readAll(t, sock) }()
	if e.wakeOverSocket(plan, b.ID) {
		t.Error("inherited session outranked its cwd mismatch and spent the recipient's attempt")
	}
	select {
	case text := <-wire:
		t.Errorf("inheritor wrote to the wrong live session: %q", text)
	case <-time.After(30 * time.Millisecond):
	}
}
