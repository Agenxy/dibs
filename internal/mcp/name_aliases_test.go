package mcp

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

// Rebuild through the production encrypted replay/event/engine construction
// path. Neither an index setter nor a test-supplied alias enters this fixture.
func aliasReplayServer(t *testing.T, dir string, limits ...core.Limits) (*httptest.Server, *engine.Engine, func()) {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "alias-fixture", box)
	if err != nil {
		t.Fatal(err)
	}
	lim := core.DefaultLimits()
	if len(limits) > 0 {
		lim = limits[0]
	}
	st := core.NewState("alias-fixture", lim)
	var history []core.Event
	led.OnEvents = func(events []core.Event) { history = append(history, events...) }
	if _, err := led.Replay(st); err != nil {
		t.Fatal(err)
	}
	led.OnEvents = nil
	eng := engine.New(st, led, nil, history)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { eng.Run(ctx); close(done) }()
	srv := httptest.NewServer(New(eng))
	var once sync.Once
	stop := func() {
		once.Do(func() {
			srv.Close()
			cancel()
			<-done
			if err := led.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(stop)
	return srv, eng, stop
}

func aliasCall(t *testing.T, srv *httptest.Server, name string, args map[string]any) map[string]any {
	t.Helper()
	r := toolCall(t, srv, name, args)
	if r["__is_error"] == true {
		t.Fatalf("%s did not succeed: %v", name, r)
	}
	return r
}

func aliasRegister(t *testing.T, srv *httptest.Server, name, nonce string) (string, string) {
	t.Helper()
	r := aliasCall(t, srv, "register", map[string]any{"name": name, "nonce": nonce})
	id, _ := r["agent_id"].(string)
	tok, _ := r["token"].(string)
	if id == "" || tok == "" {
		t.Fatalf("setup: missing identity: %v", r)
	}
	aliasCall(t, srv, "check_in", map[string]any{"token": tok})
	return id, tok
}

func aliasRename(t *testing.T, srv *httptest.Server, tok, name string) {
	t.Helper()
	r := aliasCall(t, srv, "update", map[string]any{"token": tok, "name": name})
	if r["name"] != name {
		t.Fatalf("setup: rename not applied: %v", r)
	}
}

func aliasSend(t *testing.T, srv *httptest.Server, sender, target, recipient, body string) float64 {
	t.Helper()
	r := aliasCall(t, srv, "send", map[string]any{"token": sender, "to": target, "type": "notify", "body": body})
	n, ok := r["msg_serial"].(float64)
	if !ok {
		t.Fatalf("no message serial: %v", r)
	}
	addressed, _ := r["addressed"].(string)
	if target != recipient && !strings.Contains(addressed, recipient) {
		t.Fatalf("resolved recipient is missing: %v", r)
	}
	return n
}

func aliasRead(t *testing.T, srv *httptest.Server, tok string, n float64, id, body string) {
	t.Helper()
	r := aliasCall(t, srv, "read_mail", map[string]any{"token": tok, "msg_serial": n})
	m, ok := r["message"].(map[string]any)
	if !ok || m["to"] != id || m["body"] != body {
		t.Fatalf("wrong mailbox/content: %v", r)
	}
}

func TestFormerNameAliasDeliversThroughMCP(t *testing.T) {
	srv, _, _ := aliasReplayServer(t, t.TempDir())
	id, tok := aliasRegister(t, srv, "worker-id", "former-name-worker")
	_, sender := aliasRegister(t, srv, "sender", "former-name-sender")
	// The alias is deliberately NOT the original immutable id, which already
	// routes on old production and cannot prove a newly retained name.
	aliasRename(t, srv, tok, "former-label")
	aliasRename(t, srv, tok, "current-label")
	n := aliasSend(t, srv, sender, "former-label", id, "one mailbox")
	aliasRead(t, srv, tok, n, id, "one mailbox")
}

func TestNonceFirstRegisterRenameAndNamelessRecoveryThroughMCP(t *testing.T) {
	for _, asleep := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "sleeping"}[asleep], func(t *testing.T) {
			srv, eng, _ := aliasReplayServer(t, t.TempDir())
			id, tok := aliasRegister(t, srv, "stable-id", "nonce-first-worker")
			_, sender := aliasRegister(t, srv, "sender", "nonce-first-sender")
			mail := aliasSend(t, srv, sender, id, id, "kept through reattach")
			aliasRename(t, srv, tok, "prior-label")
			if asleep {
				if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{id}}); err != nil {
					t.Fatal("setup:", err)
				}
				if got := aliasRow(t, eng, id)["status"]; got != core.StatusDormant {
					t.Fatalf("setup: sleep was not applied: %v", got)
				}
			}
			r := aliasCall(t, srv, "register", map[string]any{"name": "new-label", "nonce": "nonce-first-worker"})
			if r["agent_id"] != id || r["name"] != "new-label" {
				t.Fatalf("nonce forked instead of recovering: %v", r)
			}
			if asleep && r["reattached"] != true {
				t.Fatalf("sleeping recovery did not report its token rotation: %v", r)
			}
			if !asleep && (r["resumed"] != true || r["token"] != tok || r["reattached"] == true) {
				t.Fatalf("active retry changed its existing continuity contract: %v", r)
			}
			tok, _ = r["token"].(string)
			if tok == "" {
				t.Fatal("recovery returned no usable token")
			}
			aliasRead(t, srv, tok, mail, id, "kept through reattach")
			aliasCall(t, srv, "register", map[string]any{"nonce": "nonce-first-worker"})
			board, err := eng.Board(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			rows := board["agents"].([]map[string]any)
			if len(rows) != 2 {
				t.Fatalf("one nonce created extra rows: %v", rows)
			}
			for _, row := range rows {
				if row["id"] == id && row["name"] != "new-label" {
					t.Fatalf("nameless retry changed the name: %v", row)
				}
			}
			n := aliasSend(t, srv, sender, "prior-label", id, "former name after recovery")
			aliasRead(t, srv, tok, n, id, "former name after recovery")
		})
	}
}

func TestAliasReleaseAndRetryAreDurableThroughMCP(t *testing.T) {
	dir := t.TempDir()
	srv, eng, stop := aliasReplayServer(t, dir)
	id, tok := aliasRegister(t, srv, "worker-id", "release-worker")
	_, sender := aliasRegister(t, srv, "sender", "release-sender")
	aliasRename(t, srv, tok, "release-label")
	aliasRename(t, srv, tok, "current-label")
	n := aliasSend(t, srv, sender, "release-label", id, "before release")
	aliasRead(t, srv, tok, n, id, "before release")
	aliasCall(t, srv, "update", map[string]any{"token": tok, "release_names": []string{"release-label"}})
	before, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	aliasCall(t, srv, "update", map[string]any{"token": tok, "release_names": []string{"release-label"}})
	after, err := eng.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after["serial"] != before["serial"] {
		t.Fatalf("absent release appended another op: before=%v after=%v", before["serial"], after["serial"])
	}
	stop()
	srv, _, _ = aliasReplayServer(t, dir)
	r := toolCall(t, srv, "send", map[string]any{"token": sender, "to": "release-label", "type": "notify", "body": "must not arrive"})
	if r["__is_error"] != true {
		t.Fatalf("released alias revived after replay: %v", r)
	}
	aliasRead(t, srv, tok, n, id, "before release")
	aliasSend(t, srv, sender, id, id, "id remains an address")
}

func TestAliasShadowingAndAmbiguityThroughMCP(t *testing.T) {
	srv, _, _ := aliasReplayServer(t, t.TempDir())
	id, tok := aliasRegister(t, srv, "worker-id", "shadow-worker")
	peer, peerTok := aliasRegister(t, srv, "peer-id", "shadow-peer")
	_, sender := aliasRegister(t, srv, "sender", "shadow-sender")
	aliasRename(t, srv, tok, "shared-label")
	aliasRename(t, srv, tok, "worker-current")
	aliasRead(t, srv, tok, aliasSend(t, srv, sender, "shared-label", id, "alias first"), id, "alias first")
	aliasRename(t, srv, peerTok, "shared-label")
	aliasRead(t, srv, peerTok, aliasSend(t, srv, sender, "shared-label", peer, "current name wins"), peer, "current name wins")
	aliasRename(t, srv, peerTok, "peer-current")
	r := toolCall(t, srv, "send", map[string]any{"token": sender, "to": "shared-label", "type": "notify", "body": "ambiguous"})
	if r["code"] != "E_AMBIGUOUS_AGENT" || !strings.Contains(r["hint"].(string), id) || !strings.Contains(r["hint"].(string), peer) {
		t.Fatalf("alias collision was guessed: %v", r)
	}
	aliasRead(t, srv, tok, aliasSend(t, srv, sender, id, id, "exact id wins"), id, "exact id wins")
}

func TestAliasRebuildAndCrashBetweenRecoveryAndRenameThroughMCP(t *testing.T) {
	dir := t.TempDir()
	srv, eng, stop := aliasReplayServer(t, dir)
	id, tok := aliasRegister(t, srv, "worker-id", "restart-worker")
	_, sender := aliasRegister(t, srv, "sender", "restart-sender")
	aliasRename(t, srv, tok, "former-label")
	aliasRename(t, srv, tok, "before-crash")
	if _, err := eng.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{id}}); err != nil {
		t.Fatal("setup:", err)
	}
	// Persist exactly the existing first recovery operation, then close at
	// the boundary before the new-name update; retry must finish the rename.
	r := aliasCall(t, srv, "register", map[string]any{"name": "before-crash", "nonce": "restart-worker"})
	if r["agent_id"] != id {
		t.Fatalf("setup: recovery forked: %v", r)
	}
	stop()
	srv, eng, _ = aliasReplayServer(t, dir)
	r = aliasCall(t, srv, "register", map[string]any{"name": "after-crash", "nonce": "restart-worker"})
	if r["agent_id"] != id || r["name"] != "after-crash" {
		t.Fatalf("retry conflicted with its own row: %v", r)
	}
	tok = r["token"].(string)
	n := aliasSend(t, srv, sender, "former-label", id, "replayed alias")
	aliasRead(t, srv, tok, n, id, "replayed alias")
	resolved, err := eng.ResolveConfiguredAgent(context.Background(), "former-label")
	if err != nil || resolved != id {
		t.Fatalf("configured role alias was lost: id=%q err=%v", resolved, err)
	}
}
