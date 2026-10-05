package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

func aliasRow(t *testing.T, eng *engine.Engine, id string) map[string]any {
	t.Helper()
	b, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range b["agents"].([]map[string]any) {
		if row["id"] == id {
			return row
		}
	}
	t.Fatalf("setup: missing row %s", id)
	return nil
}

func aliasBoard(t *testing.T, eng *engine.Engine) core.Result {
	t.Helper()
	b, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal("setup:", err)
	}
	return b
}

func aliasSweep(t *testing.T, eng *engine.Engine, id string) {
	t.Helper()
	if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{id}}); err != nil {
		t.Fatal("setup:", err)
	}
}

func TestNonceRenameRefusalPrecedesRecoveryThroughMCP(t *testing.T) {
	dir := t.TempDir()
	srv, eng, stop := aliasReplayServer(t, dir)
	id, tok := aliasRegister(t, srv, "worker-id", "refused-worker")
	aliasRegister(t, srv, "taken-name", "refused-peer")
	aliasRename(t, srv, tok, "worker-label")
	aliasSweep(t, eng, id)
	if got := fmt.Sprint(aliasRow(t, eng, id)["status"]); got != "dormant" {
		t.Fatalf("setup: expected dormant, got %s", got)
	}
	before := aliasBoard(t, eng)
	r := toolCall(t, srv, "register", map[string]any{"nonce": "refused-worker", "name": "taken-name"})
	if r["code"] != "E_NAME_TAKEN" {
		t.Fatalf("name was not pre-admitted: %v", r)
	}
	after, err := eng.Board(context.Background())
	if err != nil || before["serial"] != after["serial"] || fmt.Sprint(aliasRow(t, eng, id)["status"]) != "dormant" {
		t.Fatalf("refused rename partly recovered the row: before=%v after=%v err=%v", before, after, err)
	}
	stop()
	srv, _, _ = aliasReplayServer(t, dir)
	// The original token remains usable after cold replay of the refusal.
	aliasCall(t, srv, "check_in", map[string]any{"token": tok})
}

func TestAliasLimitAndMixedReleaseEnterThroughMCP(t *testing.T) {
	dir := t.TempDir()
	srv, eng, stop := aliasReplayServer(t, dir)
	id, tok := aliasRegister(t, srv, "worker-id", "bounded-worker")
	for n := 0; n <= 64; n++ {
		// Reset the ordinary rate bucket by a real restart, not an index setter
		// or a testing exemption. Every chunk also exercises full-history replay.
		if n > 0 && n%20 == 0 {
			stop()
			srv, eng, stop = aliasReplayServer(t, dir)
		}
		aliasRename(t, srv, tok, fmt.Sprintf("label-%02d", n))
	}
	r := toolCall(t, srv, "update", map[string]any{"token": tok, "name": "label-65"})
	if r["code"] != "E_TOO_LARGE" || !strings.Contains(fmt.Sprint(r["hint"]), "release") {
		t.Fatalf("65th former name was admitted: %v", r)
	}
	before := aliasBoard(t, eng)
	r = aliasCall(t, srv, "update", map[string]any{"token": tok, "name": "label-65", "release_names": []string{"label-00"}})
	if got := aliasBoard(t, eng)["serial"].(uint64); got != before["serial"].(uint64)+1 {
		t.Fatalf("mixed release did not record exactly one ordinary update: %d", got)
	}
	if r["name"] != "label-65" {
		t.Fatalf("mixed release/rename did not commit: %v", r)
	}
	_, sender := aliasRegister(t, srv, "sender", "bounded-sender")
	aliasRead(t, srv, tok, aliasSend(t, srv, sender, "label-64", id, "new former name"), id, "new former name")
	r = toolCall(t, srv, "send", map[string]any{"token": sender, "to": "label-00", "type": "notify", "body": "released"})
	if r["code"] != "E_NO_AGENT" {
		t.Fatalf("mixed release left the old alias: %v", r)
	}
}

func TestAbsentAliasReleaseDoesNotWakeSleepingOwnerThroughMCP(t *testing.T) {
	srv, eng, _ := aliasReplayServer(t, t.TempDir())
	id, tok := aliasRegister(t, srv, "worker-id", "absent-worker")
	aliasRename(t, srv, tok, "former-label")
	aliasRename(t, srv, tok, "current-label")
	aliasCall(t, srv, "update", map[string]any{"token": tok, "release_names": []string{"former-label"}})
	aliasSweep(t, eng, id)
	if fmt.Sprint(aliasRow(t, eng, id)["status"]) != "dormant" {
		t.Fatal("setup: row was not dormant")
	}
	before := aliasBoard(t, eng)
	r := aliasCall(t, srv, "update", map[string]any{"token": tok, "release_names": []string{"former-label"}})
	after := aliasBoard(t, eng)
	if r["changed"] != false || before["serial"] != after["serial"] || fmt.Sprint(aliasRow(t, eng, id)["status"]) != "dormant" {
		t.Fatalf("absent release woke or ledgered: result=%v before=%v after=%v", r, before, after)
	}
	r = aliasCall(t, srv, "update", map[string]any{"token": tok, "release_names": []string{}})
	after = aliasBoard(t, eng)
	if r["changed"] != false || before["serial"] != after["serial"] {
		t.Fatalf("empty release recorded a fictitious effect: %v", r)
	}
	// A repeated transport host is not a change, but a real identity revision
	// must still reach the normal update path when paired with an absent release.
	r = aliasCall(t, srv, "update", map[string]any{
		"token": tok, "release_names": []string{"former-label"}, "branch": "codex/moved",
	})
	after = aliasBoard(t, eng)
	identity, _ := r["identity"].([]any)
	if len(identity) != 1 || identity[0] != "branch" || before["serial"] == after["serial"] ||
		fmt.Sprint(aliasRow(t, eng, id)["status"]) != "active" {
		t.Fatalf("absent release swallowed a real identity revision: %v", r)
	}
	before = after
	r = aliasCall(t, srv, "update", map[string]any{
		"token": tok, "release_names": []string{"former-label"}, "branch": "codex/moved",
	})
	if r["changed"] != false || before["serial"] != aliasBoard(t, eng)["serial"] {
		t.Fatalf("repeating the identity revision appended a fictitious change: %v", r)
	}
}

func TestArchivedNonceRenameAndPurgedAliasFencingThroughMCP(t *testing.T) {
	lim := core.DefaultLimits()
	lim.DormancyMax, lim.ArchiveRetention = 0, 0
	dir := t.TempDir()
	srv, eng, stop := aliasReplayServer(t, dir, lim)
	id, tok := aliasRegister(t, srv, "worker-id", "archived-worker")
	senderID, sender := aliasRegister(t, srv, "sender", "archived-sender")
	if _, err := eng.GrantRole(context.Background(), senderID, "admin"); err != nil {
		t.Fatal("setup:", err)
	}
	aliasRename(t, srv, tok, "former-label")
	aliasRename(t, srv, tok, "before-archive")
	aliasSweep(t, eng, id)
	aliasSweep(t, eng, "")
	census := aliasCall(t, srv, "all_mail", map[string]any{"token": sender, "census": true, "agent": id})
	rows, _ := census["census"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["status"] != "archived" {
		t.Fatalf("setup: expected a retained archived row, got %v", census)
	}
	r := aliasCall(t, srv, "register", map[string]any{"nonce": "archived-worker", "name": "recovered-label"})
	if r["agent_id"] != id || r["name"] != "recovered-label" {
		t.Fatalf("archived recovery forked or kept the wrong label: %v", r)
	}
	tok = r["token"].(string)
	aliasRead(t, srv, tok, aliasSend(t, srv, sender, "former-label", id, "after archive"), id, "after archive")
	aliasSweep(t, eng, id)
	aliasSweep(t, eng, "") // archive
	aliasSweep(t, eng, "") // purge after zero retention
	census = aliasCall(t, srv, "all_mail", map[string]any{"token": sender, "census": true, "agent": id})
	if rows, _ := census["census"].([]any); len(rows) != 0 {
		t.Fatal("setup: old owner was not purged")
	}
	replacement, _ := aliasRegister(t, srv, "worker-id", "replacement-worker")
	if replacement != id {
		t.Fatalf("setup: id was not reused: %s", replacement)
	}
	stop()
	srv, _, _ = aliasReplayServer(t, dir, lim)
	r = toolCall(t, srv, "send", map[string]any{"token": sender, "to": "former-label", "type": "notify", "body": "must not inherit"})
	if r["code"] != "E_NO_AGENT" {
		t.Fatalf("a reused id inherited former names: %v", r)
	}
}

type aliasLogSink struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *aliasLogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *aliasLogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestConfiguredRoleAliasesExposeShadowingThroughProductionBoard(t *testing.T) {
	var logs aliasLogSink
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	srv, eng, _ := aliasReplayServer(t, t.TempDir())
	id, tok := aliasRegister(t, srv, "worker-id", "configured-worker")
	peer, peerTok := aliasRegister(t, srv, "peer-id", "configured-peer")
	aliasRename(t, srv, tok, "configured-label")
	if resolved, err := eng.ResolveConfiguredAgent(context.Background(), "configured-label"); err != nil || resolved != id {
		t.Fatalf("setup: current configured name did not resolve: %s %v", resolved, err)
	}
	aliasRename(t, srv, tok, "worker-current")
	// A configured name can become an alias between reconciler ticks. The
	// board must diagnose it immediately, before the next role resolution.
	aliasConfiguredBoard(t, eng, "alias", id, "")
	resolved, err := eng.ResolveConfiguredAgent(context.Background(), "configured-label")
	if err != nil || resolved != id {
		t.Fatalf("configured alias lost: %s %v", resolved, err)
	}
	aliasConfiguredBoard(t, eng, "alias", id, "")
	aliasRename(t, srv, peerTok, "configured-label")
	for range 2 {
		resolved, err = eng.ResolveConfiguredAgent(context.Background(), "configured-label")
		if err != nil || resolved != peer {
			t.Fatalf("current-name precedence changed: %s %v", resolved, err)
		}
	}
	aliasConfiguredBoard(t, eng, "current", peer, id)
	if strings.Count(logs.String(), "configured role name shadows former-name aliases") != 1 || !strings.Contains(logs.String(), "level=INFO") {
		t.Fatalf("shadowing was silent or logged repeatedly: %s", logs.String())
	}
	workerIdentity, err := eng.AgentIdentity(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	peerIdentity, err := eng.AgentIdentity(context.Background(), peer)
	if err != nil || workerIdentity == peerIdentity || workerIdentity == "" || peerIdentity == "" {
		t.Fatalf("shadowing lost credential separation: err=%v", err)
	}
}

func aliasConfiguredBoard(t *testing.T, eng *engine.Engine, via, id, shadow string) {
	t.Helper()
	b, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	views, _ := wire["configured_name_addresses"].([]any)
	if len(views) != 1 {
		t.Fatalf("role address diagnostic missing from board: %s", raw)
	}
	view := views[0].(map[string]any)
	if view["name"] != "configured-label" || view["id"] != id || view["via"] != via {
		t.Fatalf("role address diagnostic is wrong: %v", view)
	}
	if shadow != "" && !strings.Contains(fmt.Sprint(view["shadowed_aliases"]), shadow) {
		t.Fatalf("shadowed alias owner omitted: %v", view)
	}
}
