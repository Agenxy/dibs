package engine

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Notices exist because a silent state change is the bug. So the thing that
// must be tested is not that the mechanism CAN deliver: it is that every event
// meaning "somebody did this to you" actually produces text, and that the text
// says what changed and what to do about it.
//
// A dropped case here is invisible: noteEvent returns quietly when nothing
// matched, which is correct for self-caused events and catastrophic for the
// rest. Nothing else in the system would report it.
func TestEveryEventDoneToAnAgentProducesANotice(t *testing.T) {
	cases := []struct {
		name string
		ev   core.Event
		want []string // fragments the agent needs in order to act
	}{{
		name: "admitted by a director after a gated declaration",
		ev: core.Event{Type: "agent.joined", Agent: "worker", Serial: 1, Data: map[string]any{
			"agent_id": "auth", "admitted_by": "director",
		}},
		want: []string{"admitted", "auth", "director"},
	}, {
		name: "promoted off an exclusive space's queue",
		ev: core.Event{Type: "agent.joined", Agent: "worker", Serial: 2, Data: map[string]any{
			"agent_id": "auth", "from_queue": true,
		}},
		want: []string{"queue", "auth", "member"},
	}, {
		// The agent it was working in no longer exists. A notice that omits that
		// leaves the agent addressing a deleted agent.
		name: "carried into another agent by a merge",
		ev: core.Event{Type: "agent.joined", Agent: "worker", Serial: 3, Data: map[string]any{
			"agent_id": "auth", "merged_from": "auth-b", "merged_by": "director",
		}},
		want: []string{"auth-b", "merged", "auth", "no longer exists"},
	}, {
		name: "still queued, but on the surviving agent after a merge",
		ev: core.Event{Type: "agent.requeued", Agent: "worker", Serial: 4, Data: map[string]any{
			"agent_id": "auth", "merged_from": "auth-b", "merged_by": "director",
			"queue_position": 2, "owner": "holder",
		}},
		want: []string{"auth-b", "no longer exists", "position 2", "holder"},
	}, {
		name: "evicted by a director",
		ev: core.Event{Type: "agent.evicted", Agent: "worker", Serial: 5, Data: map[string]any{
			"agent_id": "auth", "by": "director",
		}},
		want: []string{"removed", "auth", "director", "stop work"},
	}, {
		// Your agent absorbed another one. You did not do it, you cannot infer
		// it, and you may now owe acknowledgements you never saw arrive.
		name: "your agent absorbed another",
		ev: core.Event{Type: "agent.absorbed", Agent: "worker", Serial: 8, Data: map[string]any{
			"agent_id": "auth", "merged_from": "auth-b", "merged_by": "director", "gained": 3,
		}},
		want: []string{"auth-b", "auth", "director", "3 member"},
	}, {
		// Never a member, so "stop work there" would be nonsense. What this
		// agent needs to know is that waiting is now pointless.
		name: "removed from an agent's queue",
		ev: core.Event{Type: "agent.evicted", Agent: "worker", Serial: 7, Data: map[string]any{
			"agent_id": "auth", "by": "director", "from_queue": true,
		}},
		want: []string{"queue", "auth", "director", "will not be admitted"},
	}, {
		name: "somebody else took the agent exclusively",
		ev: core.Event{Type: "agent.exclusive", Agent: "worker", Serial: 6, Data: map[string]any{
			"agent_id": "auth", "owner": "holder",
		}},
		want: []string{"auth", "exclusive", "holder"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{}
			e.noteEvent(tc.ev)
			got := e.takeNotices("worker")
			if len(got) != 1 {
				t.Fatalf("this is a change the agent did not cause and cannot infer; "+
					"it must be told. got %d notices", len(got))
			}
			for _, frag := range tc.want {
				if !strings.Contains(got[0].Text, frag) {
					t.Errorf("notice must mention %q so the agent can act on it; got: %s",
						frag, got[0].Text)
				}
			}
		})
	}
}

// The mirror image: an agent's own actions come back as its tool result, and
// repeating them as notices trains agents to ignore the space.
func TestSelfCausedChangesProduceNoNotice(t *testing.T) {
	for _, ev := range []core.Event{
		// Joined an agent by itself: no admitted_by, no from_queue, no merge.
		{Type: "agent.joined", Agent: "worker", Data: map[string]any{"agent_id": "auth"}},
		// Took exclusivity itself.
		{Type: "agent.exclusive", Agent: "worker", Data: map[string]any{"agent_id": "auth", "owner": "worker"}},
		// Ordinary traffic, which the inbox already carries.
		{Type: "agent.post", Agent: "worker", Data: map[string]any{"agent_id": "auth"}},
	} {
		e := &Engine{}
		e.noteEvent(ev)
		if n := e.takeNotices("worker"); len(n) != 0 {
			t.Errorf("%s: self-caused, already in the tool result; got %q", ev.Type, n[0].Text)
		}
	}
}

// An agent that never polls must not grow without bound, and what survives must
// be the newest: being told you were admitted and not that you were later
// evicted is worse than being told nothing.
func TestNoticesAreBoundedAndKeepTheNewest(t *testing.T) {
	e := &Engine{}
	for i := 1; i <= maxNotices+5; i++ {
		e.noteEvent(core.Event{
			Type: "agent.evicted", Agent: "worker", Serial: uint64(i),
			Data: map[string]any{"agent_id": "auth", "by": "director"},
		})
	}
	got := e.takeNotices("worker")
	if len(got) != maxNotices {
		t.Fatalf("want %d notices, got %d", maxNotices, len(got))
	}
	if got[0].Serial != 6 || got[len(got)-1].Serial != uint64(maxNotices+5) {
		t.Fatalf("the newest must survive; got serials %d..%d", got[0].Serial, got[len(got)-1].Serial)
	}
	// Reading does not consume: the wake path is deliberately side-effect-free,
	// so that a caller it cannot identify has nothing to spend. What clears a
	// notice is the agent's own authenticated check_in.
	if n := e.takeNotices("worker"); len(n) != maxNotices {
		t.Fatalf("the wake path must be repeatable and unchanged; got %d", len(n))
	}
	e.AckNotices("worker")
	if n := e.takeNotices("worker"); n != nil {
		t.Fatalf("acknowledged notices must not come back; got %d", len(n))
	}
}

// A peer must not be able to affect what an agent is told. At all.
//
// hook_poll is token-less by necessity: a harness lifecycle hook has no token,
// so it resolves an agent by session id, and any holder of the shared coordination
// secret can name somebody else's session. Two designs failed here before this
// one:
//
//  1. DELETE on read. A peer consumed the victim's one-shot notices outright.
//  2. THROTTLE on read. A peer polling faster than the window won every
//     eligibility point and starved the victim indefinitely. Slower, not fixed,
//     and worse, it looked fixed.
//
// Both failed for the same reason: they let an unidentifiable caller SPEND
// something. The fix is not a better timeout, it is that the token-less path
// mutates nothing at all.
func TestTheTokenLessWakePathCannotSpendAnything(t *testing.T) {
	e := &Engine{}
	e.noteEvent(core.Event{
		Type: "agent.joined", Agent: "victim", Serial: 1,
		Data: map[string]any{"agent_id": "auth", "admitted_by": "director"},
	})

	// A peer hammers the victim's session. Every read must be identical, because
	// none of them may change anything.
	for i := range 50 {
		got := e.takeNotices("victim")
		if len(got) != 1 {
			t.Fatalf("poll %d: a peer changed what the victim is owed; got %d notices", i, len(got))
		}
	}
	// And the victim, polling after all of that, is told exactly the same thing.
	if got := e.takeNotices("victim"); len(got) != 1 {
		t.Fatalf("the victim must be unaffected by any number of peer polls; got %d", len(got))
	}

	// The authenticated path is what actually delivers and clears. It is the one
	// caller the daemon can identify as the agent itself.
	if got := e.pendingNotices("victim"); len(got) != 1 {
		t.Fatalf("check_in must deliver what the wake path only nudges about; got %d", len(got))
	}
	e.AckNotices("victim")
	if got := e.takeNotices("victim"); len(got) != 0 {
		t.Fatalf("a notice the agent has acknowledged must not come back; got %d", len(got))
	}
}

// An obligation you can only be PUSHED is an obligation you can lose.
//
// Announcements used to reach an agent through exactly one path: the wake
// injection in hook_poll. An agent whose harness has no plugin installed never
// saw them; one that lost its context could not ask what it owed; and because
// redelivery is rate-limited and consumed on read, a digest that arrived at a
// bad moment was gone until the next retry window. Everything else in the
// system can be pulled. This must be too.
func TestAnnouncementsCanBePulled(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	now := time.Unix(1700000000, 0)
	tok := map[string]string{}
	for _, n := range []string{"sender", "member"} {
		res, _, err := st.Apply(&core.Op{Kind: core.OpRegister, Name: n, NewToken: "tok-" + n}, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res["agent_id"].(string)
		tok[n] = id
		if _, _, err := st.Apply(&core.Op{Kind: core.OpAckBoard, Token: st.Agents[id].Token}, now); err != nil {
			t.Fatal(err)
		}
	}
	ap := func(op *core.Op) core.Result {
		t.Helper()
		r, _, err := st.Apply(op, now)
		if err != nil {
			t.Fatalf("%s: %v", op.Kind, err)
		}
		return r
	}
	ap(&core.Op{Kind: core.OpSpaceOpen, Token: st.Agents["sender"].Token, Space: "L", Text: "work"})
	ap(&core.Op{Kind: core.OpSpaceJoin, Token: st.Agents["member"].Token, Space: "L"})
	r := ap(&core.Op{Kind: core.OpSpaceAnnounce, Token: st.Agents["sender"].Token, Space: "L", Body: "FREEZE auth/retry.go"})
	serial := r["serial"].(uint64)

	owed := st.UnackedFor("member")
	if len(owed) != 1 {
		t.Fatalf("the member owes an ack and must be able to ask; got %d", len(owed))
	}
	if owed[0]["serial"] != serial || owed[0]["body"] != "FREEZE auth/retry.go" {
		t.Fatalf("the pull must carry the body, not just a count: %v", owed[0])
	}
	// It has to say what to DO: a serial with no instruction is a puzzle.
	if act, _ := owed[0]["action"].(string); !strings.Contains(act, "ack_announcement") ||
		!strings.Contains(act, fmt.Sprint(serial)) {
		t.Fatalf("the pull must name the call and the serial that clears it, got %q", act)
	}

	// Pure read: asking must not consume, or an agent that checks twice loses
	// the obligation it was checking for.
	if again := st.UnackedFor("member"); len(again) != 1 {
		t.Fatalf("reading must not consume; second read gave %d", len(again))
	}
	// The sender never owed it, and must not be told it does.
	if n := len(st.UnackedFor("sender")); n != 0 {
		t.Fatalf("the sender does not owe itself an ack, got %d", n)
	}
	// Acking clears it, through the pull path too.
	ap(&core.Op{Kind: core.OpSpaceAck, Token: st.Agents["member"].Token, MsgSerial: serial})
	if n := len(st.UnackedFor("member")); n != 0 {
		t.Fatalf("acking must clear the obligation, still %d outstanding", n)
	}
}

// check_in is the documented checkpoint after context loss: the call an agent
// makes when it has forgotten everything. If what it OWES is not in that
// answer, the recovery path is incomplete by exactly the obligation the agent
// is least able to reconstruct.
func TestAckBoardCarriesWhatYouOwe(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	now := time.Unix(1700000000, 0)
	for _, n := range []string{"sender", "member"} {
		res, _, err := st.Apply(&core.Op{Kind: core.OpRegister, Name: n, NewToken: "tok-" + n}, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res["agent_id"].(string)
		if _, _, err := st.Apply(&core.Op{Kind: core.OpAckBoard, Token: st.Agents[id].Token}, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, op := range []*core.Op{
		{Kind: core.OpSpaceOpen, Token: st.Agents["sender"].Token, Space: "L", Text: "work"},
		{Kind: core.OpSpaceJoin, Token: st.Agents["member"].Token, Space: "L"},
		{Kind: core.OpSpaceAnnounce, Token: st.Agents["sender"].Token, Space: "L", Body: "FREEZE auth/retry.go"},
	} {
		if _, _, err := st.Apply(op, now); err != nil {
			t.Fatalf("%s: %v", op.Kind, err)
		}
	}

	res, _, err := st.Apply(&core.Op{Kind: core.OpAckBoard, Token: st.Agents["member"].Token}, now)
	if err != nil {
		t.Fatal(err)
	}
	owed, _ := res["announcements"].([]core.Result)
	if len(owed) != 1 || owed[0]["body"] != "FREEZE auth/retry.go" {
		t.Fatalf("recovering agent must be told what it owes; got %v", res["announcements"])
	}
}

// hook_poll is authenticated by NOTHING.
//
// It takes a session id and a cwd off the wire with no agent token, because a
// harness lifecycle hook does not have one: that is the whole reason the
// endpoint exists. So the caller cannot prove it is the agent it names, and
// must not receive anything private on the strength of that name.
//
// It used to include 240 characters of the message body. Verified against a
// running daemon: any holder of the coordination secret, which is every agent
// configured on the machine: could pass a peer's session id, OR omit the
// session id and pass the peer's working directory, and read the peer's private
// message text. Both routes. "Mail between other agents is private to them" is
// a promise this surface broke.
//
// Raised by an independent reviewer (GPT-5.6-sol) after an earlier, incomplete
// fix of mine closed only the accidental route.
func TestTheWakePathNamesWhatIsWaitingWithoutQuotingIt(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	now := time.Unix(1700000000, 0)
	for _, n := range []string{"victim", "peer"} {
		res, _, err := st.Apply(&core.Op{Kind: core.OpRegister, Name: n, NewToken: "tok-" + n}, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res["agent_id"].(string)
		if _, _, err := st.Apply(&core.Op{Kind: core.OpAckBoard, Token: st.Agents[id].Token}, now); err != nil {
			t.Fatal(err)
		}
	}
	const secret = "SECRET the staging password is hunter2"
	if _, _, err := st.Apply(&core.Op{
		Kind: core.OpSendMessage, Token: st.Agents["peer"].Token, To: "victim",
		MsgType: "question", Body: secret, OpID: "q1", DeadlineSec: 600,
	}, now); err != nil {
		t.Fatal(err)
	}

	e := &Engine{state: st}
	lines := e.pendingMail("victim")
	if len(lines) != 1 {
		t.Fatalf("one message is waiting, got %d", len(lines))
	}
	got := lines[0]
	if strings.Contains(got, "hunter2") || strings.Contains(got, secret) {
		t.Fatalf("the body must not reach a caller that proved nothing: %q", got)
	}
	// It still has to WAKE: who, what kind, and how to fetch it. A digest that
	// says only "you have mail" leaves the agent unable to prioritise, and one
	// that says nothing at all is the silent-state bug this path exists to fix.
	for _, want := range []string{"question", "peer", "read_mail"} {
		if !strings.Contains(got, want) {
			t.Errorf("the wake must name %q so the agent can act; got %q", want, got)
		}
	}
}

// Every path that CONSUMES a notice must also DELIVER it, and this is here
// because one of them did not.
//
// Notices are cleared on token-authenticated calls, which is what keeps a peer
// on the token-less wake path from consuming them. check_in was taught to
// return them first. Inbox cleared them and returned nothing, so an ordinary
// inbox read permanently destroyed notices the agent had never seen.
//
// That is worse than the peer-DoS it was part of fixing: the agent did it to
// itself, by making a perfectly correct call, and nothing anywhere recorded
// that anything had been lost. Consuming and delivering have to be one act.
func TestEveryPathThatClearsANoticeDeliversItFirst(t *testing.T) {
	// The engine-level halves of that contract, in the order the fault occurred:
	// pendingNotices must report before AckNotices drops.
	e := &Engine{}
	e.noteEvent(core.Event{
		Type: "agent.joined", Agent: "worker", Serial: 1,
		Data: map[string]any{"agent_id": "auth", "admitted_by": "director"},
	})

	delivered := e.pendingNotices("worker")
	if len(delivered) != 1 {
		t.Fatalf("nothing to deliver before clearing; got %d", len(delivered))
	}
	e.AckNotices("worker")
	if got := e.pendingNotices("worker"); len(got) != 0 {
		t.Fatalf("clearing must be final once delivered; got %d", len(got))
	}

	// And the guard against the next caller that clears without delivering.
	//
	// Exactly ONE production site may call AckNotices: the check_in branch in
	// engine.go, which delivers first. Two owners is how this went wrong twice,
	// first Inbox cleared without returning anything, destroying unseen notices
	// on an ordinary read; then, once it returned them too, whichever of inbox
	// and check_in an agent happened to call first silently decided which
	// response carried them. Counted from the source, because the type system
	// cannot express "only one caller".
	sites := 0
	for _, f := range []string{"api.go", "engine.go", "notices.go"} {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "AckNotices(") && !strings.Contains(line, "func ") {
				sites++
			}
		}
	}
	if sites != 1 {
		t.Errorf("AckNotices is called from %d places, want exactly 1 (the check_in "+
			"branch, which delivers first): every extra caller is a place a notice "+
			"can be consumed without being shown", sites)
	}
}

// A cache keyed by agent id must forget agents that no longer exist.
//
// This is the announcement leak's sibling, found by looking for the PATTERN
// rather than the instance. Agent ids are derived from the declaration, so
// identical work reuses one; agents are now reclaimed automatically when their
// last member leaves; and the footprint cache was keyed by that id and never
// pruned. A reclaimed agent's footprint would therefore be handed to whatever
// opened the id next, matching the new agent on the OLD agent's files, and the
// "already backfilled" guard meant it never got its own. It also grew forever.
func TestTheFootprintCacheForgetsReclaimedSpaces(t *testing.T) {
	e := &Engine{footprints: map[string][]core.PredFile{
		"reclaimed":  {{Path: "old/gone.go", Weight: 1}},
		"still-here": {{Path: "live/work.go", Weight: 1}},
	}}

	e.forgetDeadFootprints(map[string]bool{"still-here": true})

	if _, stale := e.footprints["reclaimed"]; stale {
		t.Error("a reclaimed agent's footprint survived, and the next agent to take " +
			"that id would be matched on files it has nothing to do with")
	}
	if _, kept := e.footprints["still-here"]; !kept {
		t.Error("a live agent's footprint was dropped, so it must be recomputed " +
			"every declaration")
	}
}

// The case the test above does NOT cover, and the reason the first fix failed.
//
// Pruning against the live set only works if the id is absent when the prune
// runs. Reclaim an id and reopen it before the next matching pass, which is the
// ordinary case, because ids come from the declaration and identical work reuses
// them, and the id is live again, so the sweep sees nothing to clean and the
// successor inherits the dead agent's files. Reproduced against an unrelated
// successor at score 1.0.
//
// The first test supplied an already-absent id, which is the easy half: it
// proved the sweep can delete, not that deletion ever happens in time. So
// invalidation is driven by the event that ends the agent, which has no window.
func TestAReopenedSpaceIdDoesNotInheritTheOldFootprint(t *testing.T) {
	e := &Engine{footprints: map[string][]core.PredFile{
		"shared-id": {{Path: "old/gone.go", Weight: 1}},
	}}

	// The agent ends...
	e.publish([]core.Event{{
		Type: "agent.reclaimed",
		Data: map[string]any{"agent_id": "shared-id", "topic": "the old work"},
	}})
	// ...and the id is immediately taken again, so a live-set sweep would see
	// it as present and skip it.
	e.forgetDeadFootprints(map[string]bool{"shared-id": true})

	if fp, stale := e.footprints["shared-id"]; stale {
		t.Errorf("a reopened id inherited the reclaimed agent's footprint %v: its "+
			"successor is matched on files it has nothing to do with", fp)
	}
}

// A merge deletes the SOURCE agent, and its id is carried as `from`. Using
// `agent_id` would silently forget nothing, because on this event agent_id names
// the coordinator who did the merge.
func TestAMergedAwaySpaceAlsoLosesItsFootprint(t *testing.T) {
	e := &Engine{footprints: map[string][]core.PredFile{
		"absorbed": {{Path: "old/gone.go", Weight: 1}},
	}}
	e.publish([]core.Event{{
		Type: "agent.merged", Agent: "director",
		Data: map[string]any{"from": "absorbed", "into": "survivor", "by": "director"},
	}})
	if _, stale := e.footprints["absorbed"]; stale {
		t.Error("an agent merged out of existence kept its footprint")
	}
}

// An agent is named for the WORK, not for the sentence describing it.
//
// Ids are slugified topics, and the topic was the whole declaration, so an
// / An agent's SECOND task is the normal case, not an edge one.
//
// Suppression counted any membership at any score, so a faint accidental overlap
// with an agent the agent was still in (one shared file is enough) stopped it
// opening an agent for genuinely different work, and told it "you are not working
// alone" about work it had stopped doing. The bar for "you already coordinate on
// this" must be the bar used for "this is worth mentioning at all".
func TestOnlyRelevantMembershipSuppressesANewSpace(t *testing.T) {
	faint := []core.AgentMatch{{Space: "old", Score: 0.02, AlreadyIn: true}}
	if alreadyCoordinating(faint, 0.15) {
		t.Error("a faint overlap with an agent you are in must not block an agent for new work")
	}
	real := []core.AgentMatch{{Space: "old", Score: 0.40, AlreadyIn: true}}
	if !alreadyCoordinating(real, 0.15) {
		t.Error("a real overlap with an agent you are in must not spawn a duplicate")
	}
	// Somebody else's agent never suppresses: that is a match, not a membership.
	theirs := []core.AgentMatch{{Space: "theirs", Score: 0.90, AlreadyIn: false}}
	if alreadyCoordinating(theirs, 0.15) {
		t.Error("an agent you are NOT in is a suggestion, not a reason to stay silent")
	}
}

// An agent whose request is approved is told, and told what changed.
//
// It is the most consequential thing that can happen to an agent that asked for
// something: it may now do what it could not a moment ago, and short of
// re-reading a message it had already sent, nothing told it. "When you approve
// an agent's request they should be notified."
func TestTheAskerIsToldItsRequestWasApproved(t *testing.T) {
	e := &Engine{}
	e.noteEvent(core.Event{
		Type: "message.approved", Agent: "boss", To: "asker", Serial: 9,
		Data: map[string]any{"msg_serial": uint64(7), "granted": "coordinator"},
	})
	got := e.pendingNotices("asker")
	if len(got) != 1 {
		t.Fatalf("the asker was told %d things about its own approved request", len(got))
	}
	for _, want := range []string{"APPROVED", "coordinator", "boss"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the notice omits %q, so the agent knows it was approved and not "+
				"what it may now do: %s", want, got[0])
		}
	}
	// And the responder is not told about their own action.
	if n := e.pendingNotices("boss"); len(n) != 0 {
		t.Errorf("the responder was notified of its own decision: %v", n)
	}
}

// A denial is a different sentence, because the right next move is different.
func TestADenialSaysNotToRetryBlindly(t *testing.T) {
	e := &Engine{}
	e.noteEvent(core.Event{
		Type: "message.denied", Agent: "boss", To: "asker", Serial: 9,
		Data: map[string]any{"msg_serial": uint64(7)},
	})
	got := e.pendingNotices("asker")
	if len(got) != 1 || !strings.Contains(got[0], "DENIED") {
		t.Fatalf("a denied request produced %v", got)
	}
	if !strings.Contains(got[0], "retry") {
		t.Errorf("the notice does not say what not to do next, which is the only "+
			"thing distinguishing it from a bare status: %s", got[0])
	}
}

// The agents already in a space are told when somebody joins it.
//
// This notified the JOINER and nobody else, which answers "what did I just
// join" and leaves "who turned up in my space" to whoever happens to re-read
// the board. Somebody arriving in the work you are doing is the definition of a
// change you did not cause and could not infer, which is what a notice is for.
// Asked for as situational awareness, after a fleet ran for a day without
// anyone noticing a new member.
func TestMembersAreToldWhenSomebodyJoinsTheirSpace(t *testing.T) {
	st := core.NewState("test", core.DefaultLimits())
	st.Spaces["auth"] = &core.Space{
		ID: "auth",
		Members: map[string]*core.Membership{
			"incumbent": {}, "newcomer": {}, "quiet-one": {},
		},
	}
	e := &Engine{state: st}
	e.noteEvent(core.Event{
		Type: "agent.joined", Agent: "newcomer", Serial: 5,
		Data: map[string]any{"agent_id": "auth"},
	})

	for _, who := range []string{"incumbent", "quiet-one"} {
		got := e.pendingNotices(who)
		if len(got) != 1 {
			t.Fatalf("%s was told %d things about a new member of the space it is "+
				"working in", who, len(got))
		}
		if !strings.Contains(got[0], "newcomer") || !strings.Contains(got[0], "auth") {
			t.Errorf("the notice does not say who joined what: %s", got[0])
		}
	}
	// The joiner gets its own notice about joining, and must not also be told
	// that it turned up.
	for _, n := range e.pendingNotices("newcomer") {
		if strings.Contains(n, "you are in") {
			t.Errorf("the joiner was told about its own arrival: %s", n)
		}
	}
}

// An approval must wake the agent that asked, even with notices turned down.
//
// `notices_wake = false` exists so an operator can stop paying for a turn when
// somebody joins a space. Its justification, written in hookPoll, is that
// "nobody is blocked on knowing who joined a space". That is true of a join and
// false of the answer to a request: the agent asked, stopped, and is doing
// nothing else until it is told.
//
// Both were filed under one word, so turning notices down also turned off being
// woken when a human approved a role grant or a mailbox handover. The operator
// hit exactly that: they pressed Approve, both effects landed correctly in the
// ledger, the agent was never told, and they had to go and say so by hand.
func TestAnApprovalWakesTheAskerEvenWithNoticesTurnedDown(t *testing.T) {
	for _, noticesOn := range []bool{true, false} {
		name := "notices_wake on"
		if !noticesOn {
			name = "notices_wake off"
		}
		t.Run(name, func(t *testing.T) {
			e := &Engine{}
			e.SetNoticesWake(noticesOn)

			// A join: situational, and the thing the switch was written for.
			e.noteEvent(core.Event{
				Type: "agent.joined", Agent: "asker", Serial: 10,
				Data: map[string]any{"agent_id": "auth", "admitted_by": "director"},
			})
			if got := e.blockingNotices("asker"); got != 0 {
				t.Fatalf("a join counted as blocking (%d): the switch has to keep "+
					"covering the case it was written for", got)
			}

			// The human approves the request this agent sent and stopped for.
			e.noteEvent(core.Event{
				Type: "message.approved", Agent: "lael", To: "asker", Serial: 11,
				Data: map[string]any{"msg_serial": uint64(7), "granted": "coordinator"},
			})

			if got := e.blockingNotices("asker"); got != 1 {
				t.Errorf("blockingNotices = %d, want 1: an approval of your own "+
					"request is something you are waiting on, so it must reach the "+
					"wake path whatever notices_wake says", got)
			}
		})
	}
}

// And the wake path itself must act on it.
//
// blockingNotices being right is worth nothing if hookPoll still refuses to
// spend a turn on it, which is what happened: `fresh` and `blocked` were
// computed from the agent's INBOX and from a notice count the operator could
// switch off. A verdict on a message the agent SENT is in neither, so a fully
// hooked agent on the default policy was not woken either.
func TestTheWakePathSpendsATurnOnAnApproval(t *testing.T) {
	for _, c := range []struct {
		name    string
		policy  WakePhase
		notices bool
	}{
		{"the default policy", WakeAll, true},
		{"notices turned down", WakeAll, false},
		{"an operator who only wants urgent news", WakeUrgent, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := &Engine{}
			e.SetWakePolicy(c.policy)
			e.SetNoticesWake(c.notices)
			e.noteEvent(core.Event{
				Type: "message.approved", Agent: "lael", To: "asker", Serial: 11,
				Data: map[string]any{"msg_serial": uint64(7), "adopted": "old-self"},
			})
			// THE PRODUCTION EXPRESSION, not a copy of it.
			//
			// This computed `waiting` and handed it to deliverToModel as both
			// terms itself, so the real one in HookPoll could be deleted
			// outright and the test stayed green: it asserted that an approval
			// WOULD wake an agent if the wake path asked about approvals, which
			// is the thing in question. hookWakeTerms is that expression, and
			// nothing here restates it.
			//
			// Every other term is zero on purpose: no unread wakes, no
			// announcements, no notices. If the approval is not carried by
			// `waiting`, nothing else is left to carry it.
			waiting := e.blockingNotices("asker")
			if waiting == 0 {
				t.Fatalf("blockingNotices sees no approval under %q, so the check "+
					"below would pass for a wake path that ignores them entirely",
					c.name)
			}
			fresh, blocked := hookWakeTerms(0, 0, 0, waiting, false)
			if !e.deliverToModel("Stop", fresh, blocked, false) {
				t.Errorf("the wake path declined to deliver an approval under %q: the "+
					"agent asked, stopped, and has no other way to learn the answer",
					c.name)
			}
		})
	}
}

// A verdict is not evicted by a burst of ordinary notices.
//
// The Blocking flag exists so an approval reaches an agent that stopped waiting
// for it. One layer below that, the notice list keeps only the newest sixteen
// and dropped the oldest regardless of kind, so ordinary situational updates
// pushed the verdict out. That loss is unrecoverable by any other path: the
// request is terminal, so it is not pending mail in anybody's inbox and
// check_in cannot reconstruct it. The agent waits for an answer it was already
// given.
//
// Pushed directly, because the trim is what is under test and the several event
// shapes that reach it are beside the point.
func TestABlockingNoticeSurvivesABurstOfOrdinaryOnes(t *testing.T) {
	e := &Engine{}
	e.SetNoticesWake(true)

	// The approval it is waiting on, FIRST, so eviction by age takes it.
	e.pushNoticeAs("asker", "lael approved your request to be coordinator", 1, 7, true)
	if e.blockingNotices("asker") != 1 {
		t.Fatal("the approval produced no blocking notice, so this test has nothing " +
			"to protect")
	}

	for i := 2; i < 2+maxNotices*2; i++ {
		e.pushNotice("asker", "newcomer joined a space you are in", uint64(i))
	}
	if n := len(e.notices["asker"]); n != maxNotices {
		t.Fatalf("the list holds %d notices, not the %d bound, so nothing was "+
			"evicted and this cannot see the eviction it names", n, maxNotices)
	}

	if got := e.blockingNotices("asker"); got == 0 {
		t.Errorf("the approval was evicted by %d later situational notices. The "+
			"request is terminal, so it is not in any inbox and check_in cannot "+
			"reconstruct it: the agent stopped for an answer that had already been "+
			"given, and nothing will ever tell it", maxNotices*2)
	}

	// And recent news is still recent: keeping the verdict must not mean
	// keeping a stale list instead.
	got := e.notices["asker"]
	for i := 1; i < len(got); i++ {
		if got[i].Serial < got[i-1].Serial {
			t.Errorf("notices came back out of order (%d after %d): an agent reads "+
				"these as a sequence", got[i].Serial, got[i-1].Serial)
			break
		}
	}
	if newest := got[len(got)-1].Serial; newest != uint64(1+maxNotices*2) {
		t.Errorf("the newest notice is serial %d, not %d: the trim dropped recent "+
			"news to keep old news", newest, 1+maxNotices*2)
	}
}
