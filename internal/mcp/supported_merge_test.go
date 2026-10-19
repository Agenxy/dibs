// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

type supportedMergeFixture struct {
	t                  *testing.T
	srv                *httptest.Server
	e                  *engine.Engine
	version            string
	lead, source, into map[string]any
	mail               any
	outgoing           any
	dir                string
	stop               func()
}

// Real encrypted replay, writer and HTTP acceptance, including the history
// listener. Disable desktop notification BEFORE Run can re-arm old mail.
func supportedMergeServer(t *testing.T, dir string) (*httptest.Server, *engine.Engine, func()) {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "merge-fixture", box)
	if err != nil {
		t.Fatal(err)
	}
	st := core.NewState("merge-fixture", core.DefaultLimits())
	var history []core.Event
	led.OnEvents = func(events []core.Event) { history = append(history, events...) }
	if _, err := led.Replay(st); err != nil {
		t.Fatal(err)
	}
	led.OnEvents = nil
	e := engine.New(st, led, nil, history)
	e.SetHumanNotifier(desktopFixture{available: false})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	srv := httptest.NewUnstartedServer(New(e))
	srv.Listener = led.MailHistory().ServingListener(ctx, srv.Listener)
	srv.Start()
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
	return srv, e, stop
}

func newSupportedMerge(t *testing.T, version string) *supportedMergeFixture {
	t.Helper()
	dir := t.TempDir()
	srv, e, stop := supportedMergeServer(t, dir)
	f := &supportedMergeFixture{t: t, srv: srv, e: e, version: version, dir: dir, stop: stop}
	e.SetHumanNotifier(desktopFixture{available: false})
	f.lead = f.call("register", map[string]any{"name": "lead", "nonce": "lead-secret", "session_id": "coordinator-session"})
	if _, err := e.GrantRole(context.Background(), "lead", core.RoleCoordinator); err != nil {
		t.Fatal(err)
	}
	f.call("check_in", map[string]any{"token": f.lead["token"]})
	f.source = f.call("register", map[string]any{"name": "worker", "nonce": "source-secret", "harness": "Codex", "surface": "cli"})
	f.mail = f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "question", "body": "private original mail"})["msg_serial"]
	f.call("check_in", map[string]any{"token": f.source["token"]})
	f.outgoing = f.call("send", map[string]any{
		"token": f.source["token"], "to": f.lead["agent_id"], "type": "question", "body": "source outgoing question",
	})["msg_serial"]
	f.into = f.call("register", map[string]any{"name": "worker", "harness": "Codex", "surface": "cli", "session_id": "worker-session"})
	if f.source["agent_id"] == f.into["agent_id"] {
		t.Fatal("setup: no duplicate")
	}
	if _, err := e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.source["agent_id"].(string)}}); err != nil {
		t.Fatal(err)
	}
	f.call("check_in", map[string]any{"token": f.into["token"]})
	return f
}

func (f *supportedMergeFixture) raw(name string, args map[string]any) map[string]any {
	f.t.Helper()
	return recoveryCall(f.t, f.srv, f.version, name, args, map[string]any{HostMetaKey: "fixture-host"})
}

func (f *supportedMergeFixture) call(name string, args map[string]any) map[string]any {
	f.t.Helper()
	r := f.raw(name, args)
	_, hasMessage := r["message"].(map[string]any)
	if r["ok"] != true && r["token"] == nil && !hasMessage {
		f.t.Fatalf("%s failed: %v", name, r)
	}
	return r
}

func (f *supportedMergeFixture) args() map[string]any {
	return map[string]any{"token": f.lead["token"], "agent": f.source["agent_id"], "into": f.into["agent_id"], "dry_run": true}
}

func TestSupportedMergeCoordinatorPreviewCommitAndReadInheritedMail(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			f := newSupportedMerge(t, version)
			a := f.args()
			p := f.call("merge_agents", a)
			if p["fence"] == nil || p["fence"] == "" || p["preview"] != true {
				t.Fatalf("missing real preview: %v", p)
			}
			if mentions(p, "private original mail") || mentions(p, "source-secret") {
				t.Fatal("preview disclosed content or credentials")
			}
			// Unrelated traffic must not make a pair's content fence stale.
			f.call("register", map[string]any{"name": "unrelated", "nonce": "unrelated-secret"})
			a["dry_run"], a["fence"] = false, p["fence"]
			f.call("merge_agents", a)
			r := f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail})
			if !mentions(r, "private original mail") {
				t.Fatal("merge did not grant actual inherited-mail readability")
			}
			outgoing := f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.outgoing})
			if !mentions(outgoing, "source outgoing question") {
				t.Fatal("merged sender cannot read its older outgoing mail")
			}
			assertMergedHistory(f)
			r = f.raw("register", map[string]any{"name": "worker", "nonce": "source-secret"})
			if r["code"] != "E_SEAT_MERGED" || r["token"] != nil || !mentions(r, "worker-2") || !mentions(r, "lead") {
				t.Fatalf("source-only credential was not tombstoned with recovery guidance: %v", r)
			}
		})
	}
}

func TestSupportedMergeRejectsStaleFenceWithoutMovingAnything(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	a := f.args()
	p := f.call("merge_agents", a)
	f.call("send", map[string]any{"token": f.lead["token"], "to": f.source["agent_id"], "type": "notify", "body": "arrived after preview"})
	a["dry_run"], a["fence"] = false, p["fence"]
	if r := f.raw("merge_agents", a); r["code"] != "E_MERGE_STALE" {
		t.Fatalf("changed mailbox accepted: %v", r)
	}
	if r := f.raw("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail}); r["code"] == nil {
		t.Fatal("refused merge nevertheless moved mail")
	}
}

func TestSupportedMergeRefusalOffersExecutableHumanRequest(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	// A declared fork is outside coordinator authority, even when dormant.
	f.call("check_in", map[string]any{"token": f.source["token"]})
	f.call("declare", map[string]any{"token": f.source["token"], "text": "waiting work", "waiting": "review"})
	if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{f.source["agent_id"].(string)}}); err != nil {
		t.Fatal(err)
	}
	a := f.args()
	p := f.call("merge_agents", a)
	a["dry_run"], a["fence"] = false, p["fence"]
	r := f.raw("merge_agents", a)
	if r["code"] != "E_MERGE_NEEDS_HUMAN" || !mentions(r, "merge") || !mentions(r, "human") {
		t.Fatalf("no concrete human route: %v", r)
	}
}

// The matching, newer row can be manufactured by the coordinator. Neither
// an absent session nor its own session proves an independent mailbox owner.
func TestSupportedMergeCoordinatorCannotManufactureTheSurvivor(t *testing.T) {
	for _, session := range []string{"", "coordinator-session"} {
		t.Run("session="+session, func(t *testing.T) {
			f := newSupportedMerge(t, "2026-07-28")
			puppet := f.call("register", map[string]any{
				"name": "worker", "nonce": "puppet-secret", "harness": "Codex", "surface": "cli", "session_id": session,
			})
			f.call("check_in", map[string]any{"token": puppet["token"]})
			a := f.args()
			a["into"] = puppet["agent_id"]
			p := f.call("merge_agents", a)
			a["dry_run"], a["fence"] = false, p["fence"]
			if r := f.raw("merge_agents", a); r["code"] != "E_MERGE_NEEDS_HUMAN" {
				t.Fatalf("manufactured survivor accepted: %v", r)
			}
			if p["coordinator_eligible"] != false || p["human_request"] == nil {
				t.Fatalf("custody refusal absent from real preview: %v", p)
			}
			if r := f.raw("read_mail", map[string]any{"token": puppet["token"], "msg_serial": f.mail}); r["code"] == nil {
				t.Fatal("refusal nevertheless disclosed mail")
			}
			// The offered request is executable; it does not grant the coordinator
			// permission to perform the effect by itself.
			req := p["human_request"].(map[string]any)
			req["token"] = f.lead["token"]
			f.call("send", req)
		})
	}
}

func TestSupportedMergeMemberAndReversedPairAreRefused(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	a := f.args()
	member := f.call("register", map[string]any{"name": "member", "nonce": "member-secret", "session_id": "member-session"})
	f.call("check_in", map[string]any{"token": member["token"]})
	a["token"] = member["token"]
	p := f.call("merge_agents", a)
	a["dry_run"], a["fence"] = false, p["fence"]
	if r := f.raw("merge_agents", a); r["code"] != "E_NOT_COORDINATOR" {
		t.Fatalf("member executed a merge: %v", r)
	}
	a = f.args()
	a["agent"], a["into"] = f.into["agent_id"], f.source["agent_id"]
	if r := f.raw("merge_agents", a); r["code"] != "E_AGENT_LIVE" {
		t.Fatalf("reversed active source accepted: %v", r)
	}
	if _, err := f.e.Do(context.Background(), &core.Op{
		Kind: core.OpSweep, StaleAgents: []string{f.into["agent_id"].(string)},
	}); err != nil {
		t.Fatal(err)
	}
	p = f.call("merge_agents", a)
	a["dry_run"], a["fence"] = false, p["fence"]
	if r := f.raw("merge_agents", a); r["code"] != "E_MERGE_NEEDS_HUMAN" {
		t.Fatalf("reversed bound/newer source accepted: %v", r)
	}
}

func TestSupportedMergeUnknownProcessEvidenceIsNotAbsence(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	// This real server has no kernel prober. A recorded PID must therefore be
	// unknown, even after a sweep labels its row dormant.
	f.call("resume", map[string]any{"nonce": "source-secret", "resume_id": "unknown-process", "pid": 1234567})
	if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{"worker"}}); err != nil {
		t.Fatal(err)
	}
	if r := f.raw("merge_agents", f.args()); r["code"] != "E_AGENT_LIVE" {
		t.Fatal("unknown process was treated as absent")
	}
}

func assertMergedHistory(f *supportedMergeFixture) {
	f.t.Helper()
	full := historyCall(f.t, f.srv, f.version, map[string]any{
		"token": f.into["token"], "include_bodies": true, "limit": 100,
	})
	if !mentions(full, "private original mail") || !mentions(full, "source outgoing question") {
		f.t.Fatal("real history query lost incoming or sender inheritance")
	}
}

func TestSupportedMergeCoordinatorCannotRouteItsNonceIntoAnotherMailbox(t *testing.T) {
	for _, invented := range []bool{false, true} {
		t.Run(map[bool]string{false: "unbound", true: "invented-session"}[invented], func(t *testing.T) {
			assertCoordinatorSourceCredentialRefused(t, invented)
		})
	}
}

func assertCoordinatorSourceCredentialRefused(t *testing.T, invented bool) {
	t.Helper()
	f := newSupportedMerge(t, "2026-07-28")
	if invented {
		f.call("register", map[string]any{"name": "worker", "nonce": "source-secret", "session_id": "invented-independent-session"})
		f.source = f.call("register", map[string]any{"name": "worker", "nonce": "source-secret"})
		f.call("update", map[string]any{"token": f.source["token"], "release_session": true})
		if _, err := f.e.Do(context.Background(), &core.Op{Kind: core.OpSweep, StaleAgents: []string{"worker"}}); err != nil {
			t.Fatal(err)
		}
		f.call("check_in", map[string]any{"token": f.into["token"]})
	}
	stranger := f.call("register", map[string]any{"name": "private-sender", "nonce": "private-sender-nonce"})
	secret := f.call("send", map[string]any{
		"token": stranger["token"], "to": f.into["agent_id"], "type": "question",
		"body": "SYNTHETIC-SURVIVOR-PRIVATE",
	})["msg_serial"]
	a := f.args()
	plan := f.call("merge_agents", a)
	a["dry_run"], a["fence"] = false, plan["fence"]
	r := f.raw("merge_agents", a)
	if r["ok"] != true {
		t.Fatalf("setup: unexpected refusal instead of custody decision: %v", r)
	}
	// The coordinator knows the nonce it chose for the unbound source. Redirecting
	// that credential makes it the reader of the independently bound survivor.
	for _, args := range []map[string]any{
		{"name": "worker", "nonce": "source-secret"},
		{"name": "worker", "recovery_nonces": []string{"source-secret", "unknown-secret"}},
	} {
		recovered := f.raw("register", args)
		if recovered["code"] != "E_SEAT_MERGED" || recovered["token"] != nil || !mentions(recovered, "worker-2") || !mentions(recovered, "lead") {
			t.Fatalf("coordinator source credential can capture survivor containing mail %v: %v", secret, recovered)
		}
	}
	if r := f.raw("resume", map[string]any{"nonce": "source-secret", "resume_id": "source-after-merge"}); r["code"] != "E_SEAT_MERGED" || r["token"] != nil {
		t.Fatalf("resume bypassed source tombstone: %v", r)
	}
	// #373's native-spelling recovery sends all retained credentials. The
	// survivor's valid mapping must win even alongside the source tombstone.
	r = f.call("register", map[string]any{"name": "worker", "recovery_nonces": []string{"source-secret", f.into["nonce"].(string)}})
	if r["agent_id"] != f.into["agent_id"] {
		t.Fatal("canonical survivor credential was cut off by its source tombstone")
	}
}

func TestSupportedMergeAdminExplicitlyTransfersTheSourceCredential(t *testing.T) {
	f := newSupportedMerge(t, "2026-07-28")
	if _, err := f.e.GrantRole(context.Background(), "lead", core.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	a := f.args()
	plan := f.call("merge_agents", a)
	if plan["nonce_follows"] != true || !mentions(plan, "the fork's credential will open worker-2") {
		t.Fatal("admin preview did not disclose credential transfer")
	}
	a["dry_run"], a["fence"] = false, plan["fence"]
	f.call("merge_agents", a)
	if r := f.call("register", map[string]any{"name": "worker", "nonce": "source-secret"}); r["agent_id"] != f.into["agent_id"] {
		t.Fatal("admin-authorized credential did not follow")
	}
}
