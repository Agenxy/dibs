package engine

import (
	"fmt"
	"sort"

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
	Serial uint64
	Text   string
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
	switch ev.Type {
	case "agent.joined":
		who, text = ev.Agent, joinedNotice(ev)
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
	e.pushNotice(who, text, ev.Serial)
}

// pushNotice queues one thing an agent must be told, for the wake path.
//
// Split out of noteEvent because not everything an agent needs telling is a
// ledger event. A subagent that stopped working is an observation about this
// machine, made by the supervision loop, with no op behind it and no serial of
// its own, but it is exactly what a notice is FOR: something that happened to
// you which you could not have inferred.
func (e *Engine) pushNotice(who, text string, serial uint64) {
	if who == "" || text == "" {
		return
	}
	if e.notices == nil {
		e.notices = map[string][]notice{}
	}
	// Bounded: an agent that never polls must not accumulate forever. The
	// newest matter most: being told you were admitted an hour ago and then
	// evicted is worse than being told only the eviction.
	e.notices[who] = append(e.notices[who], notice{Serial: serial, Text: text})
	if n := len(e.notices[who]); n > maxNotices {
		e.notices[who] = e.notices[who][n-maxNotices:]
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

// AckNotices drops an agent's notices for good. Called on a TOKEN-authenticated
// read, which is the only place we know the agent itself is asking.
func (e *Engine) AckNotices(agent string) { delete(e.notices, agent) }
