// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

// External-package tests enter through the production writer and encrypted
// replay. This instruments the pure decisions under the core coverage gate
// without constructing an alias index or calling its setters by hand.
type nameWriter struct {
	e     *engine.Engine
	ctx   context.Context
	close func()
}

func openNameWriter(t *testing.T, dir string, limits core.Limits) nameWriter {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup:", err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "alias-coverage", box)
	if err != nil {
		t.Fatal("setup:", err)
	}
	st := core.NewState("alias-coverage", limits)
	var history []core.Event
	led.OnEvents = func(events []core.Event) { history = append(history, events...) }
	if _, err := led.Replay(st); err != nil {
		t.Fatal("setup:", err)
	}
	led.OnEvents = nil
	e := engine.New(st, led, nil, history)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
			if err := led.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(stop)
	return nameWriter{e: e, ctx: ctx, close: stop}
}

func (w nameWriter) do(t *testing.T, op *core.Op) core.Result {
	t.Helper()
	r, err := w.e.Do(w.ctx, op)
	if err != nil {
		t.Fatalf("%s failed: %v", op.Kind, err)
	}
	return r
}

func (w nameWriter) register(t *testing.T, name, nonce string) (string, string) {
	t.Helper()
	r := w.do(t, &core.Op{Kind: core.OpRegister, Name: name, Nonce: nonce})
	id, idOK := r["agent_id"].(string)
	token, tokenOK := r["token"].(string)
	if !idOK || !tokenOK || id == "" || token == "" {
		t.Fatal("setup: registration returned no identity")
	}
	w.do(t, &core.Op{Kind: core.OpAckBoard, Token: token})
	return id, token
}

func (w nameWriter) refuse(t *testing.T, op *core.Op, code string) {
	t.Helper()
	_, err := w.e.Do(w.ctx, op)
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != code {
		t.Fatalf("%s: got %v, want %s", op.Kind, err, code)
	}
}

func (w nameWriter) serial(t *testing.T) any {
	t.Helper()
	r, err := w.e.Board(w.ctx)
	if err != nil {
		t.Fatal("setup:", err)
	}
	return r["serial"]
}

func (w nameWriter) deliver(t *testing.T, sender, recipient, address string) {
	t.Helper()
	r := w.do(t, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: address,
		MsgType: core.MsgNotify, Body: "alias coverage receipt",
	})
	if note, _ := r["addressed"].(string); !strings.Contains(note, recipient) {
		t.Fatalf("send did not identify the resolved recipient: %v", r)
	}
}

// Decode the additive field rather than referencing a new Go field. The
// identical test compiles against old production and must fail behaviorally.
func releaseUpdate(t *testing.T, token, name string, names ...string) *core.Op {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"kind": core.OpUpdate, "name": name, "release_names": names})
	if err != nil {
		t.Fatal("setup:", err)
	}
	op := &core.Op{Token: token, KeepDescription: true}
	if err := json.Unmarshal(raw, op); err != nil {
		t.Fatal("setup:", err)
	}
	return op
}

func TestAliasOwnershipAndReplayThroughCoreWriterDoor(t *testing.T) {
	dir := t.TempDir()
	w := openNameWriter(t, dir, core.DefaultLimits())
	id, token := w.register(t, "worker-id", "coverage-worker")
	peer, peerToken := w.register(t, "peer-id", "coverage-peer")
	for _, name := range []string{"a-former", "z-former", "current-label"} {
		w.do(t, &core.Op{Kind: core.OpUpdate, Token: token, Name: name, KeepDescription: true})
	}
	w.deliver(t, peerToken, id, "a-former")
	mail, err := w.e.Inbox(w.ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	messages, _ := mail["messages"].([]*core.Message)
	if len(messages) != 1 || messages[0].To != id || messages[0].Body != "alias coverage receipt" {
		t.Fatalf("former name did not reach the single owned mailbox: %v", mail)
	}
	w.refuse(t, releaseUpdate(t, peerToken, "", "a-former"), "E_NOT_PERMITTED")
	for _, name := range []string{id, "current-label"} {
		w.refuse(t, releaseUpdate(t, token, "", name), "E_BAD_ARG")
	}
	w.refuse(t, &core.Op{Kind: core.OpUpdate, Token: token, Name: peer}, "E_NAME_TAKEN")
	r := w.do(t, releaseUpdate(t, token, "next-label", "z-former", "a-former", "z-former"))
	released, _ := r["released_names"].([]string)
	if !slices.Equal(released, []string{"a-former", "z-former"}) {
		t.Fatalf("effective releases were not canonical and unique: %v", r)
	}
	changed := releaseUpdate(t, token, "", "a-former")
	changed.Agent = &core.AgentInfo{Branch: "codex/coverage"}
	w.do(t, changed)
	before := w.serial(t)
	repeated := releaseUpdate(t, token, "", "a-former")
	repeated.Agent = &core.AgentInfo{Branch: "codex/coverage"}
	if r := w.do(t, repeated); r["changed"] != false || w.serial(t) != before {
		t.Fatalf("repeated identity metadata made an absent release change state: %v", r)
	}
	w.close()
	w = openNameWriter(t, dir, core.DefaultLimits())
	w.deliver(t, peerToken, id, "current-label")
	w.refuse(t, &core.Op{
		Kind: core.OpSendMessage, Token: peerToken, To: "a-former",
		MsgType: core.MsgNotify, Body: "released",
	}, "E_NO_AGENT")
	w.do(t, &core.Op{Kind: core.OpUpdate, Token: peerToken, Name: "current-label"})
	w.deliver(t, token, peer, "current-label")
	w.do(t, &core.Op{Kind: core.OpUpdate, Token: peerToken, Name: "peer-current"})
	w.refuse(t, &core.Op{
		Kind: core.OpSendMessage, Token: token, To: "current-label",
		MsgType: core.MsgNotify, Body: "ambiguous",
	}, "E_AMBIGUOUS_AGENT")
	w.do(t, releaseUpdate(t, token, "", "current-label"))
	w.deliver(t, token, peer, "current-label")
	w.do(t, &core.Op{Kind: core.OpSignOff, Token: token})
	w.refuse(t, &core.Op{Kind: core.OpRegister, Nonce: "coverage-worker"}, "E_AGENT_CLOSED")
}

func TestAliasBoundAndIncarnationFenceThroughCoreWriterDoor(t *testing.T) {
	limits := core.DefaultLimits()
	limits.DormancyMax, limits.ArchiveRetention = 0, 0
	dir := t.TempDir()
	w := openNameWriter(t, dir, limits)
	id, token := w.register(t, "worker-id", "bounded-coverage-worker")
	for n := 0; n <= 64; n++ {
		if n > 0 && n%20 == 0 {
			w.close()
			w = openNameWriter(t, dir, limits)
		}
		w.do(t, &core.Op{Kind: core.OpUpdate, Token: token, Name: fmt.Sprintf("former-%02d", n)})
	}
	before := w.serial(t)
	w.refuse(t, &core.Op{Kind: core.OpUpdate, Token: token, Name: "former-65"}, "E_TOO_LARGE")
	if w.serial(t) != before {
		t.Fatal("alias-bound refusal committed a partial rename")
	}
	w.do(t, releaseUpdate(t, token, "former-65", "former-00"))
	_, sender := w.register(t, "sender", "bounded-coverage-sender")
	w.deliver(t, sender, id, "former-64")
	w.refuse(t, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: "former-00",
		MsgType: core.MsgNotify, Body: "released",
	}, "E_NO_AGENT")
	w.close()
	w = openNameWriter(t, dir, limits)
	if resolved, err := w.e.ResolveConfiguredAgent(w.ctx, "former-01"); err != nil || resolved != id {
		t.Fatalf("cold replay lost an earlier alias: %s %v", resolved, err)
	}
	w.do(t, &core.Op{Kind: core.OpSweep, StaleAgents: []string{id}})
	w.do(t, &core.Op{Kind: core.OpSweep}) // archive after zero dormancy
	if _, err := w.e.ResolveConfiguredAgent(w.ctx, "former-01"); err == nil {
		t.Fatal("a retained archived alias was eligible for a live configured role")
	}
	w.do(t, &core.Op{Kind: core.OpSweep}) // purge after zero archive retention
	replacement, _ := w.register(t, "worker-id", "replacement-coverage-worker")
	if replacement != id {
		t.Fatalf("setup: purge did not permit id reuse: %s", replacement)
	}
	w.close()
	w = openNameWriter(t, dir, limits)
	w.refuse(t, &core.Op{
		Kind: core.OpSendMessage, Token: sender, To: "former-01",
		MsgType: core.MsgNotify, Body: "must not inherit",
	}, "E_NO_AGENT")
}
