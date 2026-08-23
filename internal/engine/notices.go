package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/agenxy/dibs/internal/core"
)

// Things that happen TO an agent, which it would otherwise never hear about.
//
// An agent that declares work under a director gate is told
// `action: "awaiting_director"` and then, until this existed, nothing: ever.
// It was admitted seconds later and had no way to know short of polling the
// event stream on the off-chance. Same for an agent queued behind an exclusive
// agent's owner: promoted when the owner left, and never told. Same for one a
// director evicted: still believes it holds the agent.
//
// All three are state changes the agent did not initiate and cannot predict,
// and all three were silent. That is the same class of bug as a declaration
// that matched nothing without saying why, and it deserves the same fix: the
// agent is TOLD, through the wake path it already has.
//
// EPHEMERAL, like announceSent and for the same reason: whether a notice has
// been shown is delivery bookkeeping, not coordination state. Writing it to the
// ledger from a read path would be an unledgered mutation. Losing it on restart
// costs at most one repeated notice about something that genuinely happened.
// maxNotices bounds what one agent can accumulate without polling. The newest
// matter most: being told you were admitted an hour ago and then evicted is
// worse than being told only the eviction.
const maxNotices = 16

type notice struct {
	// Serial is the EVENT that produced this notice, used for ordering.
	Serial uint64
	// Msg is the MESSAGE the notice points at, when it points at one, so that
	// reading that message satisfies the instruction the notice gave.
	//
	// Distinct from Serial deliberately, and the distinction is the bug this
	// field exists for: a notice saying "read_mail(746)" is produced by the
	// respond event at serial 749. Clearing by the event serial never matches
	// what the agent was told to read, so the notice survived being obeyed and
	// the wake path repeated it every turn.
	Msg  uint64
	Text string
	// Blocking marks a notice somebody is WAITING on, as opposed to one that
	// merely keeps them current.
	//
	// The distinction is load-bearing because `notices_wake = false` exists. It
	// was written for situational awareness, and its justification is the
	// comment in hookPoll: "nobody is blocked on knowing who joined a space".
	// True of a join. Not true of the answer to a request the agent SENT and
	// then stopped for: it asked, it is waiting, and by construction it is
	// doing nothing else. Filing both under one word meant an operator who
	// turned notices down to save tokens also turned off being woken when a
	// human approved a role grant or a mailbox handover, and nothing said so.
	//
	// Reported by the operator, who approved a request and then had to go and
	// tell the agent by hand.
	Blocking bool
}

// noteEvent records the events an agent needs told about, so hookPoll can
// deliver them. Called on the writer loop, from publish.
//
// Only events an agent did NOT cause: `agent.joined` for a self-service join is
// the agent's own tool result and repeating it back is noise. The distinguishing
// mark is in the data. `admitted_by` or `from_queue` mean somebody else moved
// you.
// joinedNotice distinguishes the joins somebody else caused from the ones the
// agent asked for. Returns "" for a self-service join: that is the agent's own
// tool result, and repeating it back trains agents to ignore the space.
func joinedNotice(ev core.Event) string {
	agent, _ := ev.Data["agent_id"].(string)
	if from, ok := ev.Data["merged_from"].(string); ok && from != "" {
		// The source agent is GONE. An agent told only "you joined X" would keep
		// addressing the agent it was working in, and every call would fail with a
		// agent it has no reason to think should be missing.
		by, _ := ev.Data["merged_by"].(string)
		return fmt.Sprintf(
			"agent %q was merged into agent %q by %s. %q no longer exists; "+
				"you are a member of %q, so post and announce there", from, agent, by, from, agent,
		)
	}
	if by, ok := ev.Data["admitted_by"].(string); ok && by != "" {
		return fmt.Sprintf(
			// Names the tool. This used to end "read the agent first", which is
			// advice pointing at nothing: there was no way to read an agent, and a
			// reviewing agent that took the instruction seriously had to ask a
			// human what the agent was about.
			"you were admitted to agent %q by %s: you may start; read it first with read_space(%q)",
			agent, by, agent,
		)
	}
	if q, ok := ev.Data["from_queue"].(bool); ok && q {
		return fmt.Sprintf(
			"you reached the front of the queue for agent %q and are now a member", agent,
		)
	}
	return ""
}

func (e *Engine) noteEvent(ev core.Event) {
	var who, text string
	// Whether somebody is WAITING on this notice. See notice.Blocking.
	var blocking bool
	switch ev.Type {
	case "agent.joined":
		who, text = ev.Agent, joinedNotice(ev)
		// And tell the people already in the space.
		//
		// This told the JOINER and nobody else, which answers "what did I just
		// join" and leaves the answer to "who turned up in my space" to whoever
		// re-reads the board. Somebody arriving in the space you are working in
		// is the definition of a thing that happened to you and that you could
		// not have inferred, which is what a notice is for. Asked for after a
		// fleet ran for a day without anyone noticing a new member: "agents
		// should be notified of things that concern them, including if another
		// agent joins their space."
		e.noteNewMember(ev)
	case "message.approved", "message.denied", "message.answered", "message.declined":
		// The ANSWER goes to whoever asked.
		//
		// A request approved is the single most consequential thing that can
		// happen to an agent that asked for something: it may now do what it
		// could not do a moment ago, and it had no way to learn that short of
		// re-reading a message it had already sent. Nothing told it. Reported
		// as: "when you approve an agent's request they should be notified."
		who, text, blocking = ev.To, answeredNotice(ev), true
	case "agent.absorbed":
		// Your agent just gained another space's members, its predicted footprint
		// and its outstanding announcements, which you may now be required to
		// acknowledge. You did not do this and cannot infer it.
		agent, _ := ev.Data["agent_id"].(string)
		from, _ := ev.Data["merged_from"].(string)
		by, _ := ev.Data["merged_by"].(string)
		gained, _ := ev.Data["gained"].(int)
		who, text = ev.Agent, fmt.Sprintf(
			"%s folded agent %q into %q: the agent you are in just gained %d member(s) "+
				"and anything %q had outstanding. Re-read %q before assuming it is still "+
				"the work you joined", by, from, agent, gained, from, agent,
		)
	case "agent.requeued":
		// Still waiting, but on a different agent, and told so, rather than
		// left holding a queue position in an agent that was deleted.
		agent, _ := ev.Data["agent_id"].(string)
		from, _ := ev.Data["merged_from"].(string)
		pos, _ := ev.Data["queue_position"].(int)
		owner, _ := ev.Data["owner"].(string)
		who, text = ev.Agent, fmt.Sprintf(
			"agent %q was merged into agent %q, which %s holds exclusively. %q no "+
				"longer exists and you are now position %d in %q's queue",
			from, agent, owner, from, pos, agent,
		)
	case "agent.evicted":
		agent, _ := ev.Data["agent_id"].(string)
		by, _ := ev.Data["by"].(string)
		if q, _ := ev.Data["from_queue"].(bool); q {
			// Never a member, so "stop work there" would be nonsense. What this
			// agent needs to know is that waiting is pointless now.
			who, text = ev.Agent, fmt.Sprintf(
				"%s removed you from the queue for agent %q: you are no longer waiting "+
					"for it and will not be admitted; find other work or ask %s why", by, agent, by,
			)
			break
		}
		who, text = ev.Agent, fmt.Sprintf(
			"you were removed from agent %q by %s: stop work there and coordinate before resuming", agent, by,
		)
	case "agent.exclusive":
		agent, _ := ev.Data["agent_id"].(string)
		if owner, ok := ev.Data["owner"].(string); ok && owner == ev.Agent {
			return // you took it yourself; your own tool result already said so
		} else {
			who, text = ev.Agent, fmt.Sprintf("agent %q is now exclusive to %s", agent, owner)
		}
	}
	if who == "" || text == "" {
		return
	}
	// The message this notice points at, when it points at one. ev.Serial is
	// the event; msg_serial is what the agent is told to read.
	msg, _ := ev.Data["msg_serial"].(uint64)
	e.pushNoticeAs(who, text, ev.Serial, msg, blocking)
}

// pushNotice queues one thing an agent must be told, for the wake path.
//
// Split out of noteEvent because not everything an agent needs telling is a
// ledger event. A subagent that stopped working is an observation about this
// machine, made by the supervision loop, with no op behind it and no serial of
// its own, but it is exactly what a notice is FOR: something that happened to
// you which you could not have inferred.
func (e *Engine) pushNotice(who, text string, serial uint64) {
	e.pushNoticeFor(who, text, serial, 0)
}

// pushNoticeFor is pushNotice plus the message the notice refers to.
func (e *Engine) pushNoticeFor(who, text string, serial, msg uint64) {
	e.pushNoticeAs(who, text, serial, msg, false)
}

// pushNoticeAs is pushNoticeFor plus whether somebody is waiting on it.
func (e *Engine) pushNoticeAs(who, text string, serial, msg uint64, blocking bool) {
	if who == "" || text == "" {
		return
	}
	if e.notices == nil {
		e.notices = map[string][]notice{}
	}
	// Bounded: an agent that never polls must not accumulate forever. The
	// newest matter most: being told you were admitted an hour ago and then
	// evicted is worse than being told only the eviction.
	e.notices[who] = append(e.notices[who],
		notice{Serial: serial, Msg: msg, Text: text, Blocking: blocking})
	if n := len(e.notices[who]); n > maxNotices {
		// BLOCKING ONES SURVIVE THE TRIM.
		//
		// This dropped the oldest, which is right for situational awareness and
		// wrong for a verdict. A `Blocking` notice is the answer to something
		// this agent asked and then stopped for; the request is terminal, so it
		// is not sitting in anybody's inbox and check_in cannot reconstruct it.
		// Sixteen later "somebody joined your space" notices therefore erased an
		// approval outright, and the agent waited for an answer that had already
		// been given. That is the failure the Blocking flag was added for,
		// reappearing one layer down.
		e.notices[who] = trimNotices(e.notices[who], maxNotices)
	}
}

// takeNotices returns everything an agent has outstanding, for the wake path.
//
// It used to DELETE on read, and the read path is hook_poll, which is
// token-less, because a harness lifecycle hook has no token. So any holder of
// the coordination secret could name a peer's session and consume that peer's
// one-shot notices: the peer was never told it had been admitted, promoted or
// evicted, and nothing anywhere recorded that the notice had been taken.
//
// The first fix throttled delivery instead of destroying it, which was not a
// fix. Any timeout is shared state mutated by a caller the daemon cannot
// identify, so a peer polling faster than the window wins every eligibility
// point and starves the victim indefinitely: a slower leak, not a closed one.
//
// So this path now mutates NOTHING. Reading is free, repeatable and harmless,
// which is the only property that makes a token-less endpoint safe to expose:
// there is no state for a peer to spend. What bounds repetition is the agent's
// own check_in, which delivers these (see pendingNotices) and clears them,
// and check_in is already required once per activation.
func (e *Engine) takeNotices(agent string) []notice {
	all := e.notices[agent]
	if len(all) == 0 {
		return nil
	}
	out := append([]notice(nil), all...)
	sort.Slice(out, func(i, j int) bool { return out[i].Serial < out[j].Serial })
	return out
}

// pendingNotices returns every notice an agent has outstanding, regardless of
// whether the wake path has shown it. This is the PULL path: the wake nudge is
// best-effort and a peer can interfere with it, so the agent's own
// token-authenticated call is what actually has to be complete.
func (e *Engine) pendingNotices(agent string) []string {
	all := e.notices[agent]
	if len(all) == 0 {
		return nil
	}
	sorted := append([]notice(nil), all...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Serial < sorted[j].Serial })
	out := make([]string, 0, len(sorted))
	for _, n := range sorted {
		out = append(out, n.Text)
	}
	return out
}

// clearNoticesFor drops the notices that pointed at one message, leaving the
// rest outstanding.
//
// Narrower than AckNotices deliberately: reading one message says nothing about
// the others, and clearing them all would swap a nagging channel for a lossy
// one, which is the worse of the two failures.
func (e *Engine) clearNoticesFor(agent string, serial uint64) {
	all := e.notices[agent]
	if len(all) == 0 {
		return
	}
	kept := all[:0]
	for _, n := range all {
		// Match on the MESSAGE the notice pointed at, not the event that
		// produced it. Those differ by a few serials and matching the wrong one
		// is why this cleared nothing the first time.
		if n.Msg != serial {
			kept = append(kept, n)
		}
	}
	if len(kept) == 0 {
		delete(e.notices, agent)
		return
	}
	e.notices[agent] = kept
}

// blockingNotices counts the notices somebody is WAITING on: answers to
// requests this agent sent and then stopped for.
//
// Separate from len(notices) because the operator can turn ordinary notices
// down, and must not thereby turn off being woken for an approval.
func (e *Engine) blockingNotices(agent string) int {
	n := 0
	for _, x := range e.notices[agent] {
		if x.Blocking {
			n++
		}
	}
	return n
}

// AckNotices drops an agent's notices for good. Called on a TOKEN-authenticated
// read, which is the only place we know the agent itself is asking.
func (e *Engine) AckNotices(agent string) { delete(e.notices, agent) }

// answeredNotice says what an answer to your own request means for you.
//
// The disposition alone ("approved") is not enough: the useful sentence is what
// you may now do, or what you should stop waiting for. An approval that granted
// a role or moved a mailbox says so, because those change what the next call
// will do and the agent has no other way to find out.
func answeredNotice(ev core.Event) string {
	by := ev.Agent
	serial, _ := ev.Data["msg_serial"].(uint64)
	switch ev.Type {
	case "message.approved":
		s := fmt.Sprintf("%s APPROVED your request (msg %d)", by, serial)
		if role, ok := ev.Data["granted"].(string); ok && role != "" {
			return s + fmt.Sprintf(": you now hold the %s role. Re-read the board; "+
				"calls that failed with E_NOT_%s will work now. %s",
				role, strings.ToUpper(role), staffBriefing(role))
		}
		if from, ok := ev.Data["adopted"].(string); ok && from != "" {
			return s + fmt.Sprintf(": %q's mail is now delivered to you. Call inbox: "+
				"it is there now and was not before", from)
		}
		return s + ". Read it with read_mail to see what they said"
	case "message.denied":
		return fmt.Sprintf("%s DENIED your request (msg %d). Do not retry the same ask "+
			"without new reasoning; read_mail has whatever they said", by, serial)
	case "message.declined":
		return fmt.Sprintf("%s declined to answer (msg %d): they are entitled to, and "+
			"nothing is coming. Ask somebody else or proceed without it", by, serial)
	default: // answered
		return fmt.Sprintf("%s answered your question (msg %d): read_mail(%d)",
			by, serial, serial)
	}
}

// rebuildBlockingNotices restores the notices a verdict creates, from state.
//
// Notices are engine-ephemeral by design, and the architecture's rule is that
// such a view must be REBUILDABLE. Nothing rebuilt this one: an approval became
// a blocking notice only in live publish processing, so a daemon restart
// between the approval and the asker's next turn boundary lost it outright. The
// effect stayed ledgered and correct, and the agent that asked was never told:
// hook_poll, [wake.exec] and check_in all saw nothing, and it waited
// indefinitely for news that had already happened. That is the precise failure
// the blocking notice was added to prevent, arriving through the one event
// nobody replays.
//
// FROM STATE, not from the event ring. state == fold(ledger), so a terminal
// message its asker has not consumed is exactly the set still owed, and it
// cannot drift from whatever the ring happens to still hold after eviction.
// THE ASKER'S OWN WATERMARK, not Message.Consumed. Consumed was the obvious
// field and it is about the wrong party: the RECIPIENT consumes a message when
// they answer it, so every verdict is consumed the instant it exists and the
// first version of this rebuilt nothing at all. The unit test set the field by
// hand and passed. An end-to-end run against a real daemon restart is what
// showed it, which is the whole reason for running one.
//
// AckedSerial is the awareness-gate watermark, it is replayable state, and it
// answers the actual question: the asker has caught up to everything at or
// below it, so a verdict that landed after it is news it has not had.
//
// Called once, before the loop starts, so the maps are not shared yet.
func (e *Engine) rebuildBlockingNotices() {
	if e.state == nil {
		return
	}
	owed := make([]*core.Message, 0, 8)
	for _, m := range e.state.Messages {
		if m == nil || m.From == "" || verdictEvent(m.State) == "" {
			continue
		}
		asker := e.state.Agents[m.From]
		if asker == nil || asker.Gone() {
			continue // nobody left to tell
		}
		if m.RespondedAt != 0 && m.RespondedAt <= asker.AckedSerial {
			continue // it has already caught up past this
		}
		owed = append(owed, m)
	}
	// By serial: map order is random, and the notice list keeps the newest
	// sixteen, so an unordered rebuild would drop a different set every boot.
	sort.Slice(owed, func(i, j int) bool { return owed[i].Serial < owed[j].Serial })
	for _, m := range owed {
		ev := core.Event{
			Type: verdictEvent(m.State), Agent: m.To, To: m.From,
			Serial: m.RespondedAt,
			Data:   map[string]any{"msg_serial": m.Serial},
		}
		// The same extras publish puts on the live event, so the rebuilt line
		// reads identically: an agent must not be able to tell that its board
		// restarted from the wording of its own mail.
		if m.Grant != "" && m.State == core.MsgStateApproved {
			ev.Data["granted"] = m.Grant
		}
		if m.Adopt != "" && m.State == core.MsgStateApproved {
			ev.Data["adopted"] = m.Adopt
		}
		e.pushNoticeAs(m.From, answeredNotice(ev), m.RespondedAt, m.Serial, true)
	}
}

// verdictEvent names the event a terminal state was published as, or "" when
// the state is not a verdict. Expiries and displacement are not answers and
// carry no notice.
func verdictEvent(state string) string {
	switch state {
	case core.MsgStateApproved:
		return "message.approved"
	case core.MsgStateDenied:
		return "message.denied"
	case core.MsgStateAnswered:
		return "message.answered"
	case core.MsgStateDeclined:
		return "message.declined"
	}
	return ""
}

// noteNewMember tells the existing members that somebody joined.
//
// Not the joiner, who already knows and gets joinedNotice, and not a member
// who is the joiner. One line: who, and where. Whether that matters is the
// reader's call, which is the whole posture of this product.
func (e *Engine) noteNewMember(ev core.Event) {
	// Guarded, because noteEvent runs on the event path and must not be able to
	// take the daemon down over a notice. A zero-value Engine has no state at
	// all, which is how the existing notice tests are built, and a nil map
	// dereference here would be a panic in the writer's own goroutine.
	if e.state == nil {
		return
	}
	spaceID, _ := ev.Data["agent_id"].(string)
	sp := e.state.Spaces[spaceID]
	if sp == nil {
		return
	}
	joiner := ev.Agent
	auto, _ := ev.Data["auto"].(bool)
	how := "joined"
	if auto {
		how = "was joined automatically, on a work-overlap match, to"
	}
	for member := range sp.Members {
		if member == joiner {
			continue
		}
		e.pushNotice(member,
			fmt.Sprintf("%s %s the space %q you are in", joiner, how, spaceID), ev.Serial)
	}
}

// staffBriefing tells a new role-holder what it can now DO.
//
// "Calls that failed will work now" is true and useless: it names no call. A
// coordinator was granted the role, asked to reconcile three abandoned agents,
// read `prune`'s description saying "never a peer", concluded the product could
// not do it, and told the operator so. It could: the description was written for
// ordinary agents and never mentioned the role. The powers existed and nothing
// on the wire said so.
//
// So the grant carries the briefing, because that is the one moment the agent is
// certain to be reading, and dibs://staff carries the rest.
func staffBriefing(role string) string {
	switch role {
	case core.RoleCoordinator:
		// "You still cannot READ another agent's mail" was false about the
		// capability named in the same sentence. Adoption moves a dormant
		// agent's mailbox ONTO a live one, and the point of doing that is to
		// read it. core/roles.go and dibs://staff both state the exception; the
		// briefing that a newly promoted agent is guaranteed to read denied it,
		// which is the worst place of the three to be wrong.
		return "As coordinator you are STAFF, not a louder agent: you may adopt_agent " +
			"an abandoned mailbox onto a live agent, prune a dormant peer's row and its " +
			"stale declarations, force_release a claim, and evict or close a space. You " +
			"cannot inspect a LIVE peer's mail: there is no all_mail for you, and " +
			"breadth is not intrusion. Adoption is the one exception and it is a real " +
			"one: what you adopt, you can read, so adopt only what is genuinely " +
			"abandoned and say why. Read dibs://staff before using any of them."
	default:
		return "Read dibs://staff for what the role lets you do."
	}
}

// trimNotices keeps the newest `limit`, sacrificing situational notices before
// blocking ones.
//
// Order is preserved within each group, so what an agent reads still reads
// chronologically. If there are more blocking notices than the limit, the
// newest of those win: an agent with sixteen unanswered requests has a problem
// that dropping the seventeenth verdict does not make worse.
func trimNotices(all []notice, limit int) []notice {
	if len(all) <= limit {
		return all
	}
	// BY POSITION, never by value.
	//
	// The first version selected `limit` entries and then rebuilt the result by
	// matching (Serial, Text) back against the original. That pair is not an
	// identity: supervisor notices deliberately use serial zero, so a repeated
	// stall and recovery for one observation produces entries that are equal in
	// both fields. Seventeen identical notices each matched one of the sixteen
	// selected, so all seventeen came back and every further push grew the list
	// again: a bound that fails open, in the function added to enforce it.
	//
	// Indices are unique whatever the contents are.
	keep := make([]bool, len(all))
	kept := 0
	for pass := 0; pass < 2 && kept < limit; pass++ {
		wantBlocking := pass == 0
		for i := len(all) - 1; i >= 0 && kept < limit; i-- {
			if !keep[i] && all[i].Blocking == wantBlocking {
				keep[i], kept = true, kept+1
			}
		}
	}
	out := make([]notice, 0, kept)
	for i, n := range all {
		if keep[i] {
			out = append(out, n)
		}
	}
	return out
}
