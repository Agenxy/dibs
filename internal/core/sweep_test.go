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
