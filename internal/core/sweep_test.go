package core

import (
	"strings"
	"testing"
	"time"
)

// WHY an agent stopped counting as live was computed at the moment it
// transitioned and then put only into the `agent.stale` event. A human opening
// the board later saw "out of touch" and nothing else: beside a last-contact
// time of "now", which reads as a broken board rather than a dead agent.
//
// The three cases are not interchangeable, and the third is the one that must
// never be mistaken for the others: an agent that never gave a PID has said
// nothing about a process at all.
func TestASpaceRecordsWhyItStoppedCountingAsLive(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		pid        int
		dead       bool
	}{
		{name: "its process exited", pid: 4242, dead: true, want: "process_exited"},
		{name: "it stopped checking in", pid: 4242, want: "lease_lapsed"},
		{name: "it never gave a pid", want: "idle_no_activity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewState("t", DefaultLimits())
			now := time.Unix(1700000000, 0)
			res, _, err := s.Apply(&Op{Kind: OpRegister, Name: "a", NewToken: "tok", PID: tc.pid}, now)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res["agent_id"].(string)

			op := &Op{Kind: OpSweep}
			if tc.dead {
				op.DeadAgents = []string{id}
			} else {
				op.StaleAgents = []string{id}
			}
			if _, _, err := s.Apply(op, now); err != nil {
				t.Fatal(err)
			}
			if got := s.Agents[id].StaleReason; got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
			// And the board must carry it, or the reader still cannot see it.
			var shown any
			agents, _ := s.Board()["agents"].([]map[string]any)
			for _, lm := range agents {
				shown = lm["stale_reason"]
			}
			if shown != tc.want {
				t.Fatalf("the board must show it; got %v", shown)
			}
		})
	}
}

// Coming back clears it. An agent that reattached and is working must not still
// be labelled with how it died last time.
func TestReturningClearsTheReason(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)
	res, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "a", NewToken: "tok", PID: 4242, SessionID: "sess",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res["agent_id"].(string)
	if _, _, err := s.Apply(&Op{Kind: OpSweep, DeadAgents: []string{id}}, now); err != nil {
		t.Fatal(err)
	}
	if s.Agents[id].StaleReason == "" {
		t.Fatal("precondition: it should have a reason")
	}
	// Re-registering with the same name + session is how an agent comes back.
	if _, _, err := s.Apply(&Op{
		Kind: OpRegister, Name: "a", NewToken: "tok2", SessionID: "sess",
	}, now); err != nil {
		t.Fatal(err)
	}
	if got := s.Agents[id].StaleReason; got != "" {
		t.Fatalf("a working agent must not still be labelled %q", got)
	}
}

// SPEC §7's honest-liveness rule says crash, hang and unresponsiveness are
// different facts reported as such. A CLEAN CLOSE is a fourth fact, and it was
// being reported as a crash: an agent that called sign_off finished
// deliberately and said so, yet its correspondent was told "coordination lease
// lapsed … verify before touching its directories": wrong in every clause, and
// it sends somebody to inspect work that definitively ended and released
// everything cleanly. That is the opposite of the caution the rule exists for.
func TestExpiryTellsTheTruthAboutWhyNobodyAnswered(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name, arrange string
		want          []string
		notWant       []string
	}{
		{
			name: "still working", arrange: "active",
			want: []string{"alive", "claims still stand"},
		},
		{
			name: "asleep, will see it on wake", arrange: "dormant",
			want: []string{"dormant", "on wake"},
		},
		{
			name: "went dark", arrange: "stale",
			want: []string{"lease lapsed", "verify"},
		},
		{
			name: "finished on purpose", arrange: "closed",
			want:    []string{"closed its agent", "not a crash", "nothing of its to verify"},
			notWant: []string{"lease lapsed"},
		},
		{
			name: "retired past retention", arrange: "gone",
			want:    []string{"no longer exists", "unverified"},
			notWant: []string{"lease lapsed"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewState("t", DefaultLimits())
			reg := func(name, tok, nonce string) *Agent {
				t.Helper()
				op := &Op{Kind: OpRegister, Name: name, NewToken: tok, PID: 42}
				if nonce != "" {
					op.AgentKind, op.Nonce = KindPersistent, nonce
				}
				r, _, err := s.Apply(op, now)
				if err != nil {
					t.Fatal(err)
				}
				id, _ := r["agent_id"].(string)
				if _, _, err := s.Apply(&Op{Kind: OpAckBoard, Token: s.Agents[id].Token}, now); err != nil {
					t.Fatal(err)
				}
				return s.Agents[id]
			}
			asker := reg("asker", "t1", "")
			nonce := ""
			if tc.arrange == "dormant" {
				nonce = "nonce-target-0123456789abcdef"
			}
			reg("target", "t2", nonce)
			r, _, err := s.Apply(&Op{
				Kind: OpSendMessage, Token: asker.Token, To: "target", MsgType: "question",
				Body: "may I?", OpID: "q1", DeadlineSec: 60,
			}, now)
			if err != nil {
				t.Fatal(err)
			}
			ser, _ := r["msg_serial"].(uint64)

			switch tc.arrange {
			case "stale", "dormant":
				if _, _, err := s.Apply(&Op{Kind: OpSweep, DeadAgents: []string{"target"}}, now); err != nil {
					t.Fatal(err)
				}
			case "closed":
				if _, _, err := s.Apply(&Op{Kind: OpSignOff, Token: s.Agents["target"].Token}, now); err != nil {
					t.Fatal(err)
				}
			case "gone":
				delete(s.Agents, "target")
			}
			if _, _, err := s.Apply(&Op{Kind: OpSweep}, now.Add(2*time.Hour)); err != nil {
				t.Fatal(err)
			}

			got := s.Messages[ser].ExpireDetail
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("must say %q: the sender acts on this; got: %s", w, got)
				}
			}
			for _, n := range tc.notWant {
				if strings.Contains(got, n) {
					t.Errorf("must NOT claim %q here; got: %s", n, got)
				}
			}
		})
	}
}

// Archiving is retention, not a verdict: an agent archived after going dark and
// one archived after a clean close look identical by status alone, and only
// StaleReason separates them. Getting this wrong made a crashed agent report as
// a tidy shutdown.
func TestArchivingDoesNotLaunderACrashIntoACleanClose(t *testing.T) {
	s := NewState("t", DefaultLimits())
	crashed := &Agent{ID: "crashed", Status: StatusArchived, StaleReason: "process_exited"}
	tidy := &Agent{ID: "tidy", Status: StatusArchived}
	if crashed.finishedCleanly() {
		t.Error("an agent archived after crashing did not finish cleanly")
	}
	if !tidy.finishedCleanly() {
		t.Error("an agent archived after closing itself did")
	}
	_ = s
}

// An id is an ADDRESS: it goes on the wire, into every message envelope and
// into urls, so it is restricted to ASCII. A name that survives none of that
// still needs an id, and "agent" is the fallback.
//
// That fallback was silent. An operator registering an agent as 監視者 got an
// agent called `agent`, a second got `agent-2`, and nothing anywhere said their
// names had been discarded: on a board, in mail, and in every hint that names
// an agent. Found by putting a wide-character name on a board to check column
// alignment, which is not what it was checking.
func TestANameThatCannotBecomeAnIDIsNotDiscardedSilently(t *testing.T) {
	s := NewState("t", DefaultLimits())
	now := time.Unix(1700000000, 0)

	res, _, err := s.Apply(&Op{Kind: OpRegister, Name: "監視者", NewToken: "t1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res["agent_id"].(string)
	if id != "agent" {
		t.Fatalf("precondition: nothing in that name is addressable, got %q", id)
	}
	note, _ := res["name_note"].(string)
	if note == "" {
		t.Fatal("the agent is the only party that can correct this; it must be told")
	}
	for _, want := range []string{"監視者", "agent", "ASCII"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note must say what was asked for, what was assigned, and why; "+
				"missing %q in: %s", want, note)
		}
	}

	// The name itself is kept, and the board carries it: otherwise a fleet
	// named in a non-Latin script reads `agent`, `agent-2`, `agent-3`: correct
	// addresses that identify nobody.
	if got := s.Agents[id].Name; got != "監視者" {
		t.Fatalf("the original name must survive, got %q", got)
	}
	agents, _ := s.Board()["agents"].([]map[string]any)
	var shown any
	for _, lm := range agents {
		if lm["id"] == id {
			shown = lm["display_name"]
		}
	}
	if shown != "監視者" {
		t.Fatalf("the board must show the name a human chose, got %v", shown)
	}

	// An ASCII name needs neither: no note, and no second name to render.
	res2, _, err := s.Apply(&Op{Kind: OpRegister, Name: "builder", NewToken: "t2"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, noisy := res2["name_note"]; noisy {
		t.Error("a name that works needs no explanation")
	}
	for _, lm := range agents {
		if lm["id"] == "builder" {
			if _, dup := lm["display_name"]; dup {
				t.Error("an id that already carries the name must not repeat it")
			}
		}
	}
}

// "We did not look" is a fact, and it was being written down as "we looked and
// it was dead".
//
// AlivePIDs is a positive-only set, so alive[pid] returns false for a process
// nobody probed, and that false went into the LEDGER as proc_alive, a
// permanent record of a measurement that never happened. Boot marks agents stale
// with no AlivePIDs at all; a sweep with no prober reports every pid alive.
// SPEC §7 exists to keep crash, hang and unresponsiveness distinct; an
// unprobed process is a fourth state and must not be recorded as the second.
func TestProcAliveIsOnlyRecordedWhenSomethingActuallyLooked(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name        string
		dead, stale bool
		alivePIDs   []int
		wantPresent bool
		wantValue   bool
	}{
		{name: "lease lapsed, nothing probed", stale: true, wantPresent: false},
		{name: "probed and found dead", dead: true, wantPresent: true, wantValue: false},
		{
			name: "probed alive but lease lapsed anyway", stale: true,
			alivePIDs: []int{4242}, wantPresent: true, wantValue: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewState("t", DefaultLimits())
			r, _, err := s.Apply(&Op{Kind: OpRegister, Name: "a", NewToken: "t1", PID: 4242}, now)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := r["agent_id"].(string)
			op := &Op{Kind: OpSweep, AlivePIDs: tc.alivePIDs}
			if tc.dead {
				op.DeadAgents = []string{id}
			}
			if tc.stale {
				op.StaleAgents = []string{id}
			}
			_, evs, err := s.Apply(op, now)
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, e := range evs {
				if e.Type != "agent.stale" {
					continue
				}
				v, present := e.Data["proc_alive"]
				found = true
				if present != tc.wantPresent {
					t.Fatalf("proc_alive present=%v, want %v: recording a verdict nobody "+
						"measured is worse than recording nothing", present, tc.wantPresent)
				}
				if present && v != tc.wantValue {
					t.Fatalf("proc_alive=%v, want %v", v, tc.wantValue)
				}
			}
			if !found {
				t.Fatal("expected an agent.stale event")
			}
		})
	}
}

// A purged agent's mail goes with it, because its ID goes back into use.
//
// An agent id is derived from its NAME. Purging the row after archive retention
// released that id while every message still pointed at it, so the next agent
// to register the same name was handed the same id and inherited the mailbox.
// For the human that name is the OS username, which is the one id an attacker
// can be sure of, and the retained mail is the operator's own.
func TestPurgingAnAgentTakesItsMailbox(t *testing.T) {
	s := NewState("test", DefaultLimits())
	now := time.Now()

	s.Agents["gone"] = &Agent{
		ID: "gone", Name: "gone", Kind: KindPersistent,
		Status: StatusArchived, ArchivedAt: now.Add(-s.Limits.ArchiveRetention - time.Hour),
		Nonce: "n-gone",
	}
	s.Nonces["n-gone"] = "gone"
	s.Messages[1] = &Message{
		Serial: 1, From: "peer", To: "gone", Type: MsgNotify,
		Body: "a private note for the operator", State: MsgStateDelivered,
	}
	// Mail this agent SENT to somebody live must survive: that inbox is not
	// theirs to lose.
	s.Agents["live"] = &Agent{ID: "live", Name: "live", Status: StatusActive}
	s.Messages[2] = &Message{
		Serial: 2, From: "gone", To: "live", Type: MsgNotify,
		Body: "still relevant", State: MsgStateDelivered,
	}

	if _, _, err := s.Apply(&Op{Kind: OpSweep}, now); err != nil {
		t.Fatalf("setup: sweep: %v", err)
	}

	if _, still := s.Agents["gone"]; still {
		t.Fatal("setup: the archived agent was not purged, so nothing below is about " +
			"a released id")
	}
	if _, orphan := s.Messages[1]; orphan {
		t.Error("mail addressed to a purged agent survived the purge. The id is " +
			"derived from the name and is now free: whoever registers that name " +
			"next is handed the same id and reads this")
	}
	if _, taken := s.Messages[2]; !taken {
		t.Error("a live agent's inbox lost a message because its SENDER was purged")
	}
}

// A purged agent's outbound mail names an address nobody can register.
//
// The purge above deliberately keeps what the agent SENT, because that inbox
// belongs to whoever received it. But the id is derived from the name and goes
// straight back into use, so those envelopes went on naming an address the next
// agent to take that name was handed: it appeared to have written mail it never
// sent, and a response routes by From, so answering the purged agent's question
// delivered the answer to a stranger and reported it delivered.
//
// The one path that would have caught it was the path being defeated. The
// "nobody will read this" apology reads s.Agents[m.From], and a live
// replacement makes that non-nil and not gone.
//
// The test above stops at "outbound mail survives", which locks in the setup
// and never registers a replacement, so it cannot see any of this.
func TestAPurgedAgentsOutboundMailDoesNotBecomeTheNextAgentsMail(t *testing.T) {
	s := NewState("test", DefaultLimits())
	now := time.Now()

	s.Agents["alice"] = &Agent{
		ID: "alice", Name: "alice", Kind: KindPersistent,
		Status: StatusArchived, ArchivedAt: now.Add(-s.Limits.ArchiveRetention - time.Hour),
		Nonce: "n-alice",
	}
	s.Nonces["n-alice"] = "alice"
	s.Agents["live"] = &Agent{
		ID: "live", Name: "live", Status: StatusActive, Token: "t-live",
	}
	// A question the recipient has not answered yet: the case where a response
	// is still to be routed.
	s.Messages[7] = &Message{
		Serial: 7, From: "alice", To: "live", Type: MsgQuestion,
		Body: "which branch?", State: MsgStateDelivered,
	}

	if _, _, err := s.Apply(&Op{Kind: OpSweep}, now); err != nil {
		t.Fatalf("setup: sweep: %v", err)
	}
	if _, still := s.Agents["alice"]; still {
		t.Fatal("setup: the archived agent was not purged, so this is not about a " +
			"released id")
	}
	m := s.Messages[7]
	if m == nil {
		t.Fatal("setup: the outbound question was deleted, which is a different bug " +
			"and means this test is not exercising the one it names")
	}
	if m.From == "alice" {
		t.Fatal("the purged agent's outbound mail still names `alice`, which is free " +
			"again: the next agent to register that name is handed the id, appears to " +
			"have written this, and receives the answer to a question it never asked")
	}
	if !IsRetiredSender(m.From) {
		t.Fatalf("outbound mail is from %q, which is neither the original nor a "+
			"retired address; whatever it is, agentID may hand it out", m.From)
	}
	// And that address cannot be minted from any name, however chosen.
	if got := agentID(s, m.From); got == m.From {
		t.Errorf("registering under a name that slugs to %q was handed that very "+
			"address, so the retirement bought nothing", got)
	}
	// The replacement is a different agent, and the responder is told so.
	s.Agents["alice"] = &Agent{ID: "alice", Name: "alice", Status: StatusActive}
	res, _, err := s.Apply(&Op{
		Kind: OpRespond, Token: "t-live", MsgSerial: 7,
		Disposition: "answer", Body: "the main one",
	}, now)
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if d, _ := res["delivered"].(bool); d {
		t.Error("the answer was reported delivered. Its reader was purged, and the " +
			"agent holding that name now is a different one")
	}
	if note, _ := res["note"].(string); !strings.Contains(note, "purged") {
		t.Errorf("the responder is told %q, which does not say the asker was purged "+
			"and the name has been taken by somebody else", note)
	}
}
