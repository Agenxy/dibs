package core

import (
	"cmp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// applySweep advances all time-based lifecycles and performs deterministic GC.
// Impure inputs (probe verdicts, lease-lapse decisions) arrive recorded in the
// op (SPEC §2, §7); everything else is a pure function of (state, now), so
// replay reproduces every decision. A sweep that changes nothing returns nil
// events and is not ledgered.
func (s *State) applySweep(op *Op, now time.Time) (Result, []Event, error) {
	var evs []Event
	// Probed, not assumed.
	//
	// AlivePIDs is a positive-only set, so `alive[pid]` returns false for a
	// process nobody looked at, and that false went into the LEDGER as
	// proc_alive, a permanent record claiming a measurement that never
	// happened. Boot marks agents stale with no AlivePIDs at all; a sweep with
	// no prober configured reports every pid as alive. Neither is knowledge.
	//
	// SPEC §7's whole point is that crash, hang and unresponsiveness are
	// different facts. "We did not look" is a fourth, and it must not be
	// written down as the second.
	alive := map[int]bool{}
	for _, p := range op.AlivePIDs {
		alive[p] = true
	}
	probed := func(l *Agent) (isAlive, known bool) {
		if l.PID == 0 {
			return false, false // no pid was ever given
		}
		if alive[l.PID] {
			return true, true
		}
		if slices.Contains(op.DeadAgents, l.ID) {
			return false, true
		}
		return false, false // lease lapsed, but nobody looked at the process
	}

	// Sorted, because this loop EMITS EVENTS.
	//
	// Go randomises map iteration per process, so a sweep that marked eight agents
	// stale at one serial produced those eight events in one order live and a
	// different order on cold replay. The replayed STATE was identical: this is
	// not a fold failure, but the reconstructed event stream was not, and that
	// stream is the audit history: `dibs log` and every events_since consumer
	// reads it. An audit trail that reorders itself when re-derived is not one.
	//
	// Every map range in this file that appends to evs is sorted for the same
	// reason. Ones that only mutate state are left alone: order cannot be
	// observed there, and sorting them would be cost without meaning.
	for _, agentID := range sortedKeys(s.Agents) {
		l := s.Agents[agentID]
		// Only the live statuses advance here. Closed, archived and unreachable
		// agents have nothing left to sweep: they are terminal, and adding empty
		// cases for them would imply a transition that does not exist.
		//exhaustive:ignore // terminal statuses are intentionally inert
		switch l.Status {
		case StatusActive:
			dead := slices.Contains(op.DeadAgents, l.ID)
			lapsed := slices.Contains(op.StaleAgents, l.ID)
			if !dead && !lapsed {
				continue
			}
			// An agent that never gave a PID has told us nothing about a process, so
			// "lapsed" overstates what we know. A chat surface only touches the API
			// when its human types; minutes of silence are its normal state, not a
			// death. Say idle, and let the reader draw their own conclusion.
			reason := "idle_no_activity"
			if l.PID != 0 {
				reason = "lease_lapsed"
			}
			if dead {
				reason = "process_exited"
			}
			l.StaleReason = reason
			released := s.releaseClaims(l.ID)
			// Ownership, not membership: an agent must not stay locked behind an
			// agent that stopped answering.
			evs = append(evs, s.yieldChannelOwnership(l.ID)...)
			l.AckedSerial = 0 // gate re-arms per activation (SPEC §6)
			if l.Kind == KindPersistent {
				l.Status = StatusDormant
				l.DormantSince = now
				evs = append(evs, Event{
					Type: "agent.dormant", Agent: l.ID,
					Data: map[string]any{"reason": reason},
				})
			} else {
				l.Status = StatusStale
				l.StaleSince = now
				// proc_alive only when a PID was actually given: alive[0] is the
				// zero value, so reporting it for a PID-less agent says
				// "proc_alive: false" about a process that was never claimed to
				// exist: read by a human as "it crashed" when nothing did.
				data := map[string]any{"reason": reason}
				if isAlive, known := probed(l); known {
					data["proc_alive"] = isAlive
				}
				evs = append(evs, Event{Type: "agent.stale", Agent: l.ID, Data: data})
			}
			for _, p := range released {
				evs = append(evs, Event{
					Type: "claim.expired", Agent: l.ID,
					Data: map[string]any{"path": p, "cause": "agent_" + string(l.Status)},
				})
			}
		case StatusStale:
			if now.Sub(l.StaleSince) > s.Limits.StaleGrace {
				l.Status = StatusArchived
				l.ArchivedAt = now
				l.Token, l.Nonce = "", ""
				evs = append(evs, Event{Type: "agent.archived", Agent: l.ID})
				evs = append(evs, s.departAllChannels(l.ID)...)
			}
		case StatusDormant:
			if now.Sub(l.DormantSince) > s.Limits.DormancyMax {
				l.Status = StatusArchived
				l.ArchivedAt = now
				l.Token, l.Nonce = "", ""
				evs = append(evs, Event{Type: "agent.archived", Agent: l.ID})
				evs = append(evs, s.departAllChannels(l.ID)...)
			}
		}
	}

	evs = append(evs, s.reclaimFinishedAgents()...)

	// Claim lease expiry: renewable lease + hard max life.
	var kept []*Claim
	for _, c := range s.Claims {
		if now.Sub(c.Renewed) > s.Limits.ClaimLease || now.Sub(c.Acquired) > s.Limits.ClaimMaxLife {
			evs = append(evs, Event{
				Type: "claim.expired", Agent: c.Agent,
				Data: map[string]any{"path": c.Path, "cause": "lease"},
			})
		} else {
			kept = append(kept, c)
		}
	}
	s.Claims = kept

	// Message deadline expiry with the liveness diagnosis cascade (SPEC §7).
	//
	// Sorted, like every other loop here that emits: expiring several messages in
	// one sweep produced them in map order, which differs per process, so the
	// audit stream reordered itself on replay.
	for _, serial := range sortedKeys(s.Messages) {
		m := s.Messages[serial]
		if m.Terminal() || !m.Expecting() || m.Deadline.IsZero() || now.Before(m.Deadline) {
			continue
		}
		to := s.Agents[m.To]
		switch {
		case to != nil && to.Status == StatusActive:
			m.State = MsgStateExpiredSilent
			m.ExpireDetail = "recipient alive but did not answer; their claims still stand"
		case to != nil && to.Status == StatusDormant:
			m.State = MsgStateExpiredDormant
			m.ExpireDetail = "recipient is dormant; it will see this on wake (past deadline, within retention bounds)"
		// A clean close is a FOURTH fact, and it was being reported as a crash.
		//
		// SPEC §7's whole point is that crash, hang and unresponsiveness are
		// different facts reported as such, but an agent that called sign_off
		// finished deliberately and SAID so, and telling its correspondent
		// "coordination lease lapsed … verify before touching its directories"
		// is wrong in every clause. It sends somebody to inspect work that
		// definitively ended and released everything cleanly, which is the
		// opposite of the caution the honest-liveness rule is for.
		//
		// The coarse state stays `expired_recipient_dead`: from the sender's
		// side the recipient is equally unavailable either way, and inventing a
		// wire state for the distinction would churn six files to say something
		// the detail already carries. It is the DETAIL a sender acts on.
		case to != nil && to.finishedCleanly():
			m.State = MsgStateExpiredDead
			m.ExpireDetail = "recipient closed its agent before answering; it finished deliberately " +
				"and released its claims, so this is not a crash and there is nothing of its to " +
				"verify: nobody will answer this now"
		case to == nil:
			m.State = MsgStateExpiredDead
			m.ExpireDetail = "recipient's agent no longer exists: retired or pruned past its retention " +
				"bound. Whether it finished or crashed is no longer recorded, so treat any " +
				"directories it held as unverified"
		default:
			m.State = MsgStateExpiredDead
			m.ExpireDetail = "recipient's coordination lease lapsed and its claims no longer stand; " +
				"this is loss of coordination, not proof its work stopped: verify before touching its directories"
		}
		m.TerminalAt = now
		evs = append(evs, Event{
			Type: "message." + m.State, Agent: m.To, To: m.From,
			Data: map[string]any{"msg_serial": m.Serial, "detail": m.ExpireDetail},
		})
	}

	// Announcements nobody acknowledged, after the retry budget ran out.
	//
	// SPEC-CHANNELS.md §10.6: silence is never resolution. The announcement is
	// NOT deleted and NOT quietly settled: it is marked `unacked` and stays on
	// the board, because "three agents never answered this" is exactly the thing
	// a human needs to see. Dropping it would make the board look calm while the
	// fleet was uncoordinated.
	for _, serial := range op.GiveUpAnnounce {
		a := s.Announcements[serial]
		if a == nil || a.State != AnnounceOpen {
			continue
		}
		a.State = AnnounceUnacked
		var silent []string
		for id := range a.Required {
			if !a.Acked[id] {
				silent = append(silent, id)
			}
		}
		sort.Strings(silent)
		evs = append(evs, Event{Type: "agent.announce_unacked", Agent: a.From, Data: map[string]any{
			"agent_id": a.Space, "serial": a.Serial, "silent": silent,
			"detail": "redelivery gave up; this is loss of coordination, not agreement. " +
				"verify with these agents before assuming they acted on it",
		}})
	}

	gcEvents, pruned := s.gc(now)
	evs = append(evs, gcEvents...)
	evs = append(evs, s.gcBlobs(now, 0)...) // TTL/cap blob eviction (A5)

	// Ledger the sweep if it MUTATED anything, not only if it announced
	// something.
	//
	// gc deletes consumed mail and expired dedup records without emitting an
	// event: correctly, since nobody needs telling that a message the sender
	// already read has aged out. But the test here was "did we emit", so a sweep
	// whose only work was those deletions returned changed:false, was never
	// written to the ledger, and did not advance the serial. Replay therefore
	// never performed the deletion: consumed mail RESURRECTED on restart, and
	// state stopped being fold(ledger): the one claim this whole design exists
	// to keep. Silent to an observer and silent to the ledger are different
	// things, and only the first is a choice.
	if len(evs) == 0 && !pruned {
		return Result{"changed": false}, nil, nil
	}
	s.finish(&evs, now)
	return Result{"changed": true}, evs, nil
}

// gc prunes replayable state deterministically (SPEC §4, §11): consumed
// terminal messages; unconsumed terminal beyond per-agent retention (advancing
// the recipient's loss watermark); archived agents past retention (with their
// nonces and dedup records); dedup records past the lesser-of bound.
func (s *State) gc(now time.Time) ([]Event, bool) {
	var evs []Event
	// pruned records mutation that emits no event. See applySweep: a deletion the
	// ledger never hears about is a deletion replay will not repeat.
	pruned := false

	annEvents := s.gcAnnouncements()
	evs = append(evs, annEvents...)

	// Consumed terminal messages go immediately; unconsumed terminal beyond
	// retention are evicted oldest-first with the watermark advanced.
	perAgent := map[string][]*Message{}
	for serial, m := range s.Messages {
		if m.Terminal() && m.Consumed && now.Sub(m.TerminalAt) > s.Limits.ConsumedRetention {
			delete(s.Messages, serial) // sender had its read window (real-agent finding)
			pruned = true
			continue
		}
		if m.Terminal() {
			perAgent[m.To] = append(perAgent[m.To], m)
		}
	}
	for _, agent := range sortedKeys(perAgent) {
		ms := perAgent[agent]
		if len(ms) <= s.Limits.TerminalRetention {
			continue
		}
		sortMessages(ms)
		evict := ms[:len(ms)-s.Limits.TerminalRetention]
		l := s.Agents[agent]
		for _, m := range evict {
			delete(s.Messages, m.Serial)
			if l != nil && m.Serial >= l.TruncatedBefore {
				l.TruncatedBefore = m.Serial + 1
			}
		}
		if l != nil {
			evs = append(evs, Event{
				Type: "mailbox.truncated", Agent: agent,
				Data: map[string]any{"truncated_before_serial": l.TruncatedBefore, "evicted": len(evict)},
			})
		}
	}

	// Archived agents past retention: remove agent, nonce, dedup records.
	for _, id := range sortedKeys(s.Agents) {
		l := s.Agents[id]
		if l.Status != StatusArchived {
			continue
		}
		// Retention counts from the ledgered archival itself (replayable),
		// never from the earlier dormant/stale transition.
		if l.ArchivedAt.IsZero() || now.Sub(l.ArchivedAt) <= s.Limits.ArchiveRetention {
			continue
		}
		delete(s.Agents, id)
		for nonce, lid := range s.Nonces {
			if lid == id {
				delete(s.Nonces, nonce)
			}
		}
		for k, rec := range s.Dedup {
			if rec.Agent == id {
				delete(s.Dedup, k)
			}
		}
		// AND THE MAIL ADDRESSED TO IT.
		//
		// An agent id is derived from its NAME, so purging the row released
		// that id for reuse while every message still pointed at it: the next
		// agent to register the same name was handed the same id and inherited
		// the mailbox. For the human that name is the OS username, which is the
		// one id an attacker can be certain of, and the retained mail is the
		// operator's own.
		//
		// Only mail TO the purged agent. What it SENT belongs to whoever
		// received it, and deleting that would take a live agent's inbox with
		// somebody else's retirement.
		mail := 0
		for serial, m := range s.Messages {
			if m.To == id {
				delete(s.Messages, serial)
				mail++
			}
		}
		evs = append(evs, Event{
			Type: "agent.purged", Agent: id,
			Data: map[string]any{"messages_dropped": mail},
		})
	}

	// Dedup records: lesser of window and per-agent cap (SPEC §4).
	counts := map[string][]*DedupRec{}
	for k, rec := range s.Dedup {
		if now.Sub(rec.At) > s.Limits.DedupWindow {
			delete(s.Dedup, k)
			pruned = true
			continue
		}
		counts[rec.Agent] = append(counts[rec.Agent], rec)
	}
	// Sorted by agent so eviction order does not depend on map iteration.
	for _, agent := range sortedKeys(counts) {
		recs := counts[agent]
		if len(recs) <= s.Limits.DedupPerAgent {
			continue
		}
		// Tie-broken by ID, because timestamps collide.
		//
		// Records come out of a map, so equal At values left them in random order
		// and the cap kept an ARBITRARY subset: a different one live than on
		// replay. Which retry is deduplicated then depends on map iteration, and
		// two runs of the same ledger disagree about whether an op already
		// happened. Idempotency that is only sometimes idempotent is worse than
		// none, because callers are told the retry was safe.
		slices.SortFunc(recs, func(a, b *DedupRec) int {
			if c := a.At.Compare(b.At); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
		for _, rec := range recs[:len(recs)-s.Limits.DedupPerAgent] {
			delete(s.Dedup, dedupKey(rec.Agent, rec.ID))
			pruned = true
		}
	}
	return evs, pruned
}

// Board is the public snapshot (presentation fields are added by the engine).
func (s *State) Board() map[string]any {
	agents := make([]map[string]any, 0, len(s.Agents))
	for _, l := range s.Agents {
		if l.Status == StatusArchived || l.Status == StatusClosed {
			continue
		}
		slots := make([]Slot, 0, len(l.Slots))
		for _, sl := range l.Slots {
			slots = append(slots, sl)
		}
		slices.SortFunc(slots, func(a, b Slot) int { return strings.Compare(a.ID, b.ID) })
		lm := map[string]any{
			"id": l.ID, "kind": l.Kind, "name": l.Name, "description": l.Description,
			"status": l.Status, "activation": l.Activation, "slots": slots,
			"last_coordination_at": l.LastCoordination,
		}
		// Role and parent, only when they are not the default. A `role: "member"`
		// on every agent and a `parent: ""` on almost all of them is payload the
		// reader and the model both pay for and neither uses.
		if l.Role != "" && l.Role != RoleMember {
			lm["role"] = l.Role
			// Whether that role is held by somebody who can come back.
			//
			// An agent registered with neither a nonce nor a session id cannot
			// be reattached by anyone, ever, and a ROLE held by such an agent is
			// a power the board says is filled and nobody can use. Shown only
			// for roles, because that is where it changes what a reader should
			// do; it names no credential, only whether one exists.
			if l.Nonce == "" && l.SessionID == "" {
				lm["unreachable"] = true
			}
		}
		// Why it stopped counting as live. Without this the board says "out of
		// touch" beside a last-contact time of "now" and leaves the reader to
		// guess whether the agent died or the board is broken.
		if l.StaleReason != "" {
			lm["stale_reason"] = l.StaleReason
		}
		// The name a human chose, when the id could not carry it. Without this a
		// board of agents named in a non-Latin script reads `agent`, `agent-2`,
		// `agent-3`: technically correct addresses that identify nobody.
		if l.Name != "" && slug(l.Name) == "" {
			lm["display_name"] = l.Name
		}
		if l.Parent != "" {
			lm["parent"] = l.Parent
		}
		// Only when known: an empty object on every agent is payload the reader
		// and the model both pay for and neither uses.
		if l.Agent != nil {
			lm["agent"] = l.Agent
		}
		agents = append(agents, lm)
	}
	slices.SortFunc(agents, func(a, b map[string]any) int {
		return strings.Compare(a["id"].(string), b["id"].(string))
	})
	claims := make([]*Claim, len(s.Claims))
	copy(claims, s.Claims)
	slices.SortFunc(claims, func(a, b *Claim) int { return strings.Compare(a.Path, b.Path) })
	spaces := s.channelBoard()
	out := map[string]any{"serial": s.Serial, "node": s.NodeID, "agents": agents, "claims": claims}
	if len(spaces) > 0 {
		out["spaces"] = spaces
	}
	return out
}

// spaceSaid is the agent's announcement history for the board, newest last,
// bounded so one chatty agent cannot dominate the payload.
//
// METADATA ONLY. This carried `body`, and the board it is embedded in is not
// the operator's alone: Board() is what check_in returns to EVERY agent on
// every activation, and /api/board serves it to anything holding the
// coordination secret. So every announcement body in every space was handed
// to agents that had joined none of them: the same defect as the agent.post
// event, on a wider surface, and reachable without even asking for it.
//
// Nothing consumed the body. The board renderer shows counts (unacked,
// abandoned, blocked); no template, script or Go caller read `said[].body`. It
// was cost with no reader, and the text still belongs to the agent: members and
// subscribers get it from read_space, which checks who is asking.
func (s *State) spaceSaid(id string) []map[string]any {
	var all []*Announcement
	for _, a := range s.Announcements {
		if a.Space == id {
			all = append(all, a)
		}
	}
	if len(all) == 0 {
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Serial < all[j].Serial })
	if len(all) > 20 {
		all = all[len(all)-20:]
	}
	out := make([]map[string]any, 0, len(all))
	for _, a := range all {
		out = append(out, map[string]any{
			"serial": a.Serial, "from": a.From,
			"bytes": len(a.Body),
			"at":    a.MadeAt, "state": a.State,
			// How many still owe it: the count a reader needs to know whether
			// this is settled or still hanging over the agent.
			"owed": len(a.Required) - len(a.Acked),
		})
	}
	return out
}

// channelBoard renders the spaces half of the board payload.
//
// Under "spaces", NOT "agents": the wire name for a space is "agent"
// (SPEC-CHANNELS.md §1) but that key has meant "the agents" since v1, and
// quietly changing what it holds would break every existing reader. The rename
// lands with the Agent→Agent pass.
func (s *State) channelBoard() []map[string]any {
	out := make([]map[string]any, 0, len(s.Spaces))
	for _, id := range s.channelIDs() {
		ch := s.Spaces[id]
		cm := map[string]any{
			"id": ch.ID, "topic": ch.Topic, "members": s.memberBoard(ch),
			"opened_by": ch.OpenedBy, "opened_at": ch.OpenedAt,
		}
		if ch.Owner != "" {
			cm["owner"] = ch.Owner
		}
		if len(ch.Queue) > 0 {
			cm["queue"] = ch.Queue
		}
		// What was actually SAID in the agent, for the human's board.
		//
		// The board carried membership and an unacked COUNT, which tells an
		// operator that something is outstanding and not what it is. A human
		// could join an agent, broadcast into it, and see "1 awaiting ack": with
		// no way anywhere in the interface to read the announcement they had
		// just sent, or the ones the agents had. read_space gave agents that and
		// the board never called it.
		//
		// Bodies, not a count, because the whole reason a human joins an agent is
		// to see what the agents are saying to each other. The board is already
		// behind the admin password (SECURITY.md): it shows decrypted mail, and
		// agent traffic is no more private than that.
		if said := s.spaceSaid(ch.ID); len(said) > 0 {
			cm["said"] = said
		}
		// Outstanding announcements are the one piece of space state that is
		// actionable at a glance, so it belongs on the board rather than behind
		// a second call (§10.6: silence is never resolution).
		waiting, abandoned, blocked := s.unackedIn(ch.ID)
		if waiting > 0 {
			cm["unacked_announcements"] = waiting
		}
		// A third state, because "still asking" and "asking somebody who is not
		// there" look identical on a board and are not the same problem.
		// Redelivery is driven by the agent polling, so an announcement owed
		// only by sleeping or crashed agents never spends its retry budget and
		// never reaches `abandoned`: it waits forever, looking healthy.
		if blocked > 0 {
			cm["blocked_announcements"] = blocked
		}
		// Members that left an agent still owing an acknowledgement. Their
		// requirement has to be dropped: waiting on somebody who is never
		// coming back is how a board fills with things nobody can act on: but
		// the fact that they never read it is not thereby untrue.
		if n := s.departedUnackedIn(ch.ID); n > 0 {
			cm["departed_unacked"] = n
		}
		// Reported separately, and never folded into the same number: one means
		// "Dibs is still asking", the other means "Dibs gave up and nobody
		// answered". Only the second needs a human.
		if abandoned > 0 {
			cm["abandoned_announcements"] = abandoned
		}
		out = append(out, cm)
	}
	return out
}

// memberBoard renders a space's membership, each entry carrying WHY it is
// there: the explainability §10.3 requires, on the board itself rather than
// fetched on demand.
func (s *State) memberBoard(ch *Space) []map[string]any {
	out := make([]map[string]any, 0, len(ch.Members))
	for _, a := range sortedKeys(ch.Members) {
		m := ch.Members[a]
		mm := map[string]any{"agent": m.Agent, "auto": m.Auto}
		if m.Score > 0 {
			mm["score"] = m.Score
			mm["threshold"] = m.Threshold
			mm["scorer"] = m.ScorerID
		}
		if len(m.Evidence) > 0 {
			mm["evidence"] = m.Evidence
		}
		out = append(out, mm)
	}
	return out
}

// unackedIn counts announcements in a space that nobody has settled, split by
// whether Dibs is still trying.
//
// The split matters and its absence was a bug. Only `open` was counted, so an
// announcement that exhausted its retries and was marked `unacked` VANISHED
// from the board: at exactly the moment it became most interesting. The
// constant's own comment claims it "stays visible, never dropped"; it did not.
//
// `abandoned` is the more urgent number: somebody was told something with
// collision risk, never acknowledged it, and Dibs has stopped asking. Nobody
// is coming back to that on their own.
func (s *State) unackedIn(space string) (waiting, abandoned, blocked int) {
	for _, ser := range s.announcementSerials() {
		a := s.Announcements[ser]
		if a.Space != space {
			continue
		}
		switch a.State {
		case AnnounceOpen:
			waiting++
			// Waiting on somebody who is not there is a different fact, and the
			// one a person may have to act on.
			//
			// Redelivery is driven by the agent POLLING: an agent that is
			// asleep or crashed never polls, so its retry budget never spends
			// and the announcement never reaches `unacked`. It sits at
			// "awaiting ack" forever while the board gives no hint that the
			// thing it waits on is gone. A standing role can legitimately sleep
			// for a week, so this is not an error; it is a fact the reader
			// cannot otherwise get without cross-referencing the roster.
			if s.blockedOnAbsentee(a) {
				blocked++
			}
		case AnnounceUnacked:
			abandoned++
		}
	}
	return waiting, abandoned, blocked
}

// departedUnackedIn counts members that left this agent owing an acknowledgement.
func (s *State) departedUnackedIn(space string) int {
	n := 0
	for _, ser := range s.announcementSerials() {
		if a := s.Announcements[ser]; a.Space == space {
			n += len(a.DepartedUnacked)
		}
	}
	return n
}

// blockedOnAbsentee reports whether every agent still owing this announcement
// is one that is not currently working.
func (s *State) blockedOnAbsentee(a *Announcement) bool {
	absent := false
	for id := range a.Required {
		if a.Acked[id] {
			continue
		}
		l := s.Agents[id]
		if l != nil && l.Status == StatusActive {
			return false // somebody who could answer still might
		}
		absent = true
	}
	return absent
}

// sortedKeys gives map iteration a fixed order for anything that reaches a
// board payload or an event.
// sortedKeys is generic over the key type because the maps that need
// deterministic traversal are keyed by both agent id and message serial, and a
// second near-identical helper would be one more place to forget.
func sortedKeys[K cmp.Ordered, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// agentID derives an addressable id from an agent's chosen name.
//
// The id is an ADDRESS: it goes on the wire, into every message envelope and
// into urls, so it is restricted to ASCII. A name that survives none of that
// still needs an id, and "agent" is the fallback.
//
// That fallback was silent, and should not be: an operator registering an agent
// as 監視者 got an agent called `agent`, a second got `agent-2`, and nothing said
// their names had been discarded. The original survives in Agent.Name; the
// registration result now says when the id owes nothing to it, and the board
// shows the name so a human can still tell who they are looking at.
func agentID(s *State, name string) string {
	base := slug(name)
	if base == "" {
		base = "agent"
	}
	id := base
	for i := 2; ; i++ {
		if _, ok := s.Agents[id]; !ok {
			return id
		}
		id = base + "-" + itoa(i)
	}
}

func slug(in string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(in)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_', r == '/':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func itoa(i int) string { return strconv.Itoa(i) }

// siblingByName finds the live agent sharing this name that the caller most
// needs to hear about. Closed and archived agents are ignored: they hold no mail
// anyone is waiting on, so reusing their name is not a collision worth
// reporting.
//
// An agent HOLDING MAIL always wins over one that is merely newer. That is the
// whole point of the warning: naming the mailbox the caller cannot read. An
// earlier version ranked purely by recency and, in the exact scenario this was
// written for, pointed at an empty sibling while the lost answer sat in an older
// one. Among equals, newest wins.
func (s *State) siblingByName(name, exclude string) *Agent {
	var best *Agent
	var bestMail int
	for _, l := range s.Agents {
		if l.ID == exclude || l.Name != name {
			continue
		}
		if l.Status == StatusClosed || l.Status == StatusArchived {
			continue
		}
		mail := len(s.Inbox(l.ID))
		if best == nil || mail > bestMail ||
			(mail == bestMail && l.CreatedSerial > best.CreatedSerial) {
			best, bestMail = l, mail
		}
	}
	return best
}

// gcAnnouncements bounds settled announcement history per space.
//
// This was the one collection in replayed state with no bound. Announcements
// were added on every announce and removed only when an empty auto-opened
// space was reclaimed, and a standing space a human opened is never
// reclaimed, so its history grew for the life of the board and was replayed into
// memory on every daemon start.
//
// ONLY fully acknowledged announcements are eligible. An `open` one is an
// outstanding obligation somebody still owes, and `unacked` is documented as
// staying visible forever precisely because redelivery gave up on it: dropping
// either would discard the fact the mechanism exists to preserve. Bounding the
// settled ones costs nothing anybody is waiting on.
//
// Deterministic, like the rest of the fold: eligible announcements are ordered by
// serial and the oldest go first, so a replay prunes identically.
func (s *State) gcAnnouncements() []Event {
	keep := s.Limits.AnnouncementRetention
	if keep <= 0 {
		return nil
	}
	settled := map[string][]*Announcement{}
	for _, a := range s.Announcements {
		if a.State == AnnounceAcked {
			settled[a.Space] = append(settled[a.Space], a)
		}
	}
	var evs []Event
	for _, ch := range sortedKeys(settled) {
		as := settled[ch]
		if len(as) <= keep {
			continue
		}
		sort.Slice(as, func(i, j int) bool { return as[i].Serial < as[j].Serial })
		evict := as[:len(as)-keep]
		for _, a := range evict {
			delete(s.Announcements, a.Serial)
		}
		evs = append(evs, Event{
			Type: "space.history_truncated", Agent: ch,
			Data: map[string]any{
				"evicted": len(evict), "kept": keep,
				"oldest_kept_serial": as[len(as)-keep].Serial,
			},
		})
	}
	return evs
}
