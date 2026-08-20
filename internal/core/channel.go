package core

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Spaces: the semantic half of coordination. See SPEC-CHANNELS.md.
//
// NAMING, because it will otherwise confuse everyone who reads this file:
// SPEC-CHANNELS.md §1 renames the participant to "agent" and gives the word
// "agent" to the space. The PROTOCOL uses that vocabulary already: the tools
// are `join_space`, `announce`: but the Go identifier for a participant is
// still `Agent`, because renaming it touches 15 files and 7 wire-format JSON
// tags, and that is a mechanical pass of its own rather than something to smuggle
// into a new subsystem. So in this file: `Space` is an agent, and `Agent` is an
// agent. The wire names are the ones that must never drift, and they are right.
//
// What a space adds over directory claims: claims detect two agents naming the
// same PATH, which is the collision that is cheap to detect rather than the one
// that hurts. Two agents refactoring one concept in two languages never name the
// same path and destroy each other's work anyway.

// PredFile is one file a piece of work is predicted to touch, and how strongly.
//
// This mirrors overlap.File deliberately rather than importing it: internal/core
// is the replayable state machine and must not depend on a package that shells
// out to git and loads models. The conversion happens at the edge, in the
// engine, which is also where the prediction is made.
type PredFile struct {
	Path   string  `json:"path"`
	Weight float64 `json:"weight"`
}

// Space is a topic of work that agents join.
type Space struct {
	ID    string
	Topic string
	// Auto records that LANES opened this agent from a declaration, rather than an
	// agent or a human opening it on purpose.
	//
	// It decides whether the agent can be reclaimed when it empties, and the two
	// answers are both right for their own case. A standing agent. "release",
	// "security review": outlives its members by design: agents drop in and out
	// and must find the same agent with its history. An agent opened automatically
	// for one declaration has no such claim, and reclaiming those is what keeps a
	// fleet from exhausting MaxAgents on work that finished hours ago.
	//
	// Without the distinction the codebase believed both at once: one test
	// asserted an empty agent persists, reclaimFinishedAgents deleted exactly those
	// agents, and the test passed only because it never swept. A standing agent
	// really did disappear at the next tick.
	Auto bool
	// Key is the agent's coordination key: Dibs' own record that the agents in
	// here decided to work together. See coordkey.go: it is issued at open,
	// held by membership, and is the one identity claim Dibs can actually
	// verify.
	Key     string
	Members map[string]*Membership // agent id → how and why they joined
	Subs    map[string]bool        // subscribers: see traffic, never collide
	// Posts is the agent's remark history, newest last, bounded by
	// Limits.PostRetention.
	//
	// Posts used to be stored nowhere. post appended the text to an event
	// and returned a serial, and that event was the only copy: an agent that
	// was not polling at that moment never saw it, a restart lost it, and
	// read_space, the tool whose whole job is "read the agent", did not return
	// posts at all. It looked like it worked because the event reached
	// everybody, including agents who had no business receiving it.
	//
	// Keeping them here is what makes post a message rather than a
	// notification: the event says a post happened, and a member who was
	// asleep, or who has just replayed after a crash, can still read what was
	// said. Announcements have always worked this way; posts now match.
	Posts []Post
	Owner string   // exclusive owner; "" when the space is open
	Queue []string // agents waiting on an exclusive space, in order
	// Pending holds what each queued agent would have joined WITH.
	//
	// Queue is ids in order, which is all the wire needs. But the join op that
	// put an agent there carries its whole provenance. Auto, Score, Threshold,
	// ScorerID/Version, Evidence, and promotion used to fabricate a fresh
	// Membership{ScorerID: "queue"} instead. An agent auto-matched at 0.71 with
	// evidence therefore surfaced, after waiting, as a manual member with score
	// 0 and no reason: indistinguishable from somebody who simply asked to be
	// there, and SPEC-CHANNELS.md §10.3's promise that every auto-join is
	// explainable was false for anything that passed through a queue.
	Pending  map[string]*Membership `json:"-"`
	OpenedBy string
	OpenedAt time.Time

	// Declined remembers agents that left this agent DELIBERATELY.
	//
	// An agent that walks out and is put straight back has not been coordinated
	// with; it has been overruled. Reported from a live fleet by an agent that
	// left an agent it did not belong in and posted its reasons: "my very next
	// declare auto-joined me again, score UP from 0.1651 to 0.2289, same generic
	// evidence." Re-adding it overrode a decision instead of making one.
	//
	// This records leave_space only. An eviction is somebody else's decision, and a
	// departure the sweep made on behalf of a crashed agent is not a decision at
	// all: neither should stop that agent being matched here later.
	//
	// It blocks AUTO-join, not membership: join_space still works, because an agent
	// is allowed to change its mind, and the agent is still surfaced with its score
	// so it can. What it will not do is happen by itself, twice.
	Declined map[string]bool

	// Predicted is the agent's file footprint: what the work in here touches,
	// merged from every member's recorded prediction.
	//
	// RECORDED, never computed: same rule as Membership.Score and for the same
	// reason (SPEC-CHANNELS.md §4.3). A footprint recomputed at replay time
	// against a reindexed repository is a different footprint, which would match
	// different agents into different agents and make the ledger a work of
	// fiction. Apply merges what the op carries and nothing else.
	Predicted []PredFile
}

// mergePredicted folds a new prediction into the agent's footprint, keeping the
// strongest weight per path.
//
// Sorted by path on the way out, because this is replayable state: a footprint
// whose order depends on map iteration would differ between two replays of one
// ledger, which is precisely what the hash chain exists to catch.
func mergePredicted(existing, add []PredFile) []PredFile {
	if len(add) == 0 {
		return existing
	}
	merged := make(map[string]float64, len(existing)+len(add))
	for _, f := range existing {
		merged[f.Path] = f.Weight
	}
	for _, f := range add {
		if f.Weight > merged[f.Path] {
			merged[f.Path] = f.Weight
		}
	}
	out := make([]PredFile, 0, len(merged))
	for p, w := range merged {
		out = append(out, PredFile{Path: p, Weight: w})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		return out[i].Path < out[j].Path
	})
	// Bounded: an agent that has accumulated a thousand files predicts nothing in
	// particular, and every future match against it would be noise.
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

// Membership records how an agent came to be in a space.
//
// Every impure field here. Score, Threshold, ScorerID, ScorerVersion, Evidence
// is COPIED FROM THE OP, never computed. That is the whole replay contract
// (SPEC-CHANNELS.md §4.3): a similarity score recomputed next week against a
// reindexed repository is a different number, so a state machine that scores
// during replay reconstructs different membership and the ledger's hash chain
// stops meaning anything. Same discipline as liveness probe verdicts (SPEC §7).
//
// It doubles as the explainability record §10.3 requires: "why am I in this
// agent" is answered from here, without re-running a model that may no longer
// exist.
type Membership struct {
	Agent         string
	Score         float64
	Threshold     float64
	ScorerID      string
	ScorerVersion string
	Evidence      []string
	Auto          bool // joined by score rather than by asking
	JoinedSerial  uint64
	// Predicted is the footprint this agent declared when it joined OR queued.
	//
	// Carried on the membership because a queued agent has one and is not yet a
	// member: the space's own footprint must not grow until the agent is
	// actually in. It used to be dropped at that point instead of deferred, so
	// an agent promoted off the queue contributed nothing to what the space
	// was understood to touch, and every later match against that space scored
	// against a footprint missing a full member's files.
	Predicted []PredFile
}

// Announcement is space traffic that must be acknowledged.
//
// A Post (posts.go) is the other half of the pair, and the distinction is the
// point: "everyone must know this" and "for the record" have to be different
// messages, because collapsing them teaches agents to ignore both. An
// announcement tracks who owes an acknowledgement and re-pings them; a post
// tracks nothing and is simply readable.
//
// This comment used to say posts were "an event and nothing more, because
// giving it state would mean tracking delivery for traffic nobody has to
// answer". The conclusion did not follow: storing a remark so it can be read
// later is not tracking its delivery, and without it the only copy of a post
// was the event, which is how posts came to be broadcast to the entire board
// and readable by nobody afterwards.
type Announcement struct {
	Serial   uint64
	Space    string
	From     string
	Body     string
	Acked    map[string]bool
	Required map[string]bool // members at announce time, excluding the sender
	Retries  int
	// DepartedUnacked names members that left the agent still owing an
	// acknowledgement: closed, pruned, or evicted before reading it.
	//
	// Their requirement has to be dropped or the announcement waits forever on
	// somebody who is never coming back. But dropping it silently made the
	// announcement settle as `acked`, which is a claim that everyone read it.
	// Observed: an announcement with acked=[] (literally nobody) recorded as
	// acked, and invisible on the board. That is the strongest guarantee this
	// system offers reporting success for something that did not happen.
	//
	// Appended in op order, so replay reproduces it exactly.
	DepartedUnacked []string
	State           string // open | acked | unacked
	MadeAt          time.Time
}

// Announcement states. An announcement is `open` until every member it named
// has acknowledged it, and `unacked` only if redelivery gave up, which is
// visible on the board rather than silent (SPEC-CHANNELS.md §10.6).
const (
	AnnounceOpen    = "open"
	AnnounceAcked   = "acked"
	AnnounceUnacked = "unacked" // gave up retrying; stays visible, never dropped
)

// Op kinds for spaces. Wire names follow SPEC-CHANNELS.md, not the Go types.
const (
	OpSpaceOpen      = "open_space"
	OpSpaceJoin      = "join_space"
	OpSpaceLeave     = "leave_space"
	OpSpaceSubscribe = "watch_space"
	OpSpaceExclusive = "lock_space"
	OpSpacePost      = "post"
	OpSpaceAnnounce  = "announce"
	OpSpaceAck       = "ack_announcement"
	OpSpaceRetitle   = "retitle_space"

	// Director powers: the coordinator role applied to spaces (§8.1).
	OpSpaceForceRelease = "unlock_space"
	OpSpaceEvict        = "evict"
	OpSpaceMerge        = "merge_spaces"
	OpSpaceClose        = "close_space"
	OpSpaceAdmit        = "admit"
)

// applySpaceRetitle replaces a space's topic, so a member can redact it.
//
// There was no way to change a topic at all. An agent in a private repository
// declared richly, as dibs://skills tells it to, and the wording became a
// durable board object; the only remedy it could find was destroying the space,
// which also destroys the coordination the space exists for. Its operator had
// to notice the leak, which is not a check anybody should depend on a human for.
//
// Any MEMBER may retitle, not only the opener. The agent that needs to redact is
// whichever one wrote something it should not have, and in an auto-opened space
// the opener and the author are the same agent anyway. Membership is the
// existing boundary for reading a space; it is the right one for editing its
// label.
//
// The old topic is NOT recorded in the result or the event. Every other op here
// reports what changed, and doing that faithfully would republish the exact
// string somebody just asked to remove, into the ledger and the activity feed.
func (s *State) applySpaceRetitle(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	ch := s.Spaces[op.Space]
	if ch == nil {
		return nil, nil, errf("E_NO_SPACE", "check the id on the board", "no space %q", op.Space)
	}
	if ch.Members[l.ID] == nil {
		return nil, nil, errf("E_NOT_A_MEMBER",
			"join the space first: its members are who may change what it says about itself",
			"agent %q is not a member of space %q", l.ID, op.Space)
	}
	// The same-topic refusal is at the engine's ingress, NOT here: a rule added
	// to the fold binds every retitle already in a ledger. See
	// Engine.refuseNoOpRetitle.
	ch.Topic = op.Text
	evs := []Event{{
		Type: "agent.retitled", Agent: l.ID,
		// The NEW topic only, never the old one. See above.
		Data: map[string]any{"agent_id": op.Space, "topic": op.Text},
	}}
	serial := s.finish(&evs, now)
	return Result{"ok": true, "space": op.Space, "topic": op.Text, "serial": serial}, evs, nil
}

// ── open ─────────────────────────────────────────────────────────────────

func (s *State) applySpaceOpen(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	if l.AckedSerial == 0 {
		return nil, nil, ErrMustAck
	}
	id := cleanID(op.Space)
	if id == "" {
		return nil, nil, errf("E_BAD_ARG", "give the agent a name", "agent id required")
	}
	if len(op.Text) > s.Limits.MaxBodyBytes {
		return nil, nil, errTooLarge("topic", s.Limits.MaxBodyBytes)
	}
	if _, exists := s.Spaces[id]; exists {
		return nil, nil, errf("E_SPACE_EXISTS", "join it instead of opening it", "space %s already exists", id)
	}
	if len(s.Spaces) >= s.Limits.MaxAgents {
		return nil, nil, errf("E_AGENT_LIMIT",
			"an agent Dibs opened from a declaration is reclaimed once its last member "+
				"leaves, so leave_space the ones you are done with. An agent somebody "+
				"opened by name outlives its members on purpose: standing agents are "+
				"the point, so leaving one frees nothing: merge_spaces it into the agent "+
				"that is the same work",
			"%d agents (max)", len(s.Spaces))
	}
	ch := &Space{
		ID: id, Topic: op.Text, Key: coordKey(s.NodeID, s.Serial), Auto: op.Auto,
		Members: map[string]*Membership{}, Subs: map[string]bool{},
		OpenedBy: l.ID, Predicted: mergePredicted(nil, op.Predicted),
	}
	s.Spaces[id] = ch
	evs := []Event{{Type: "agent.opened", Agent: l.ID, Data: map[string]any{
		"agent_id": id, "topic": op.Text,
	}}}
	// The opener is a member. An agent whose creator is not in it is an agent
	// nobody is working in, which is not a thing worth having.
	ch.Members[l.ID] = &Membership{
		Agent: l.ID, ScorerID: "explicit", JoinedSerial: s.Serial + 1,
	}
	if op.Exclusive {
		ch.Owner = l.ID
		evs = append(evs, Event{Type: "agent.exclusive", Agent: l.ID, Data: map[string]any{
			"agent_id": id, "owner": l.ID,
		}})
	}
	s.finish(&evs, now)
	ch.OpenedAt = evs[0].TS
	// The key goes back to the agent that just earned it. A key nobody is ever
	// handed is a mechanism nobody can use, which is what "the join path is
	// decorative" meant.
	return Result{
		"agent_id": id, "exclusive": op.Exclusive, "key": ch.Key,
		"key_hint": "declare this in `refs` on later declare calls and Dibs will " +
			"match you to this agent exactly, instead of guessing from your wording",
	}, evs, nil
}

// ── join ─────────────────────────────────────────────────────────────────

// memberFromOp builds the membership record a join op describes.
//
// One place, because the same record has to be produced from three:
// an immediate join, a promotion off the queue, and a merge. The two latter
// used to fabricate their own with a placeholder ScorerID and no score, which
// silently voided §10.3's explainability guarantee for every agent that did not
// join an agent the instant it asked.
func memberFromOp(agent string, op *Op, serial uint64) *Membership {
	m := &Membership{
		Agent: agent, Score: op.Score, Threshold: op.Threshold,
		ScorerID: op.ScorerID, ScorerVersion: op.ScorerVersion,
		Evidence: op.Evidence, Auto: op.Auto, JoinedSerial: serial,
		Predicted: mergePredicted(nil, op.Predicted),
	}
	if m.ScorerID == "" {
		m.ScorerID = "explicit" // an agent that asked, rather than a score
	}
	return m
}

// promote turns a queued agent into a member, keeping the provenance it was
// queued with rather than inventing a fresh one.
func (ch *Space) promote(agent string, serial uint64) {
	m := ch.Pending[agent]
	if m == nil {
		// Queued before Pending existed, or by a path that recorded nothing.
		// Say "queue" honestly rather than claim a score we never had.
		m = &Membership{Agent: agent, ScorerID: "queue"}
	}
	m.JoinedSerial = serial
	delete(ch.Pending, agent)
	ch.Members[agent] = m
	// The footprint the agent declared while waiting now counts, because the
	// agent now counts.
	ch.Predicted = mergePredicted(ch.Predicted, m.Predicted)
}

func (s *State) applySpaceJoin(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	if l.AckedSerial == 0 {
		return nil, nil, ErrMustAck
	}
	ch := s.Spaces[cleanID(op.Space)]
	if ch == nil {
		return nil, nil, errf("E_NO_SPACE", "open_space it, or read the board for open spaces", "no space %s", op.Space)
	}
	if _, already := ch.Members[l.ID]; already {
		return Result{"agent_id": ch.ID, "joined": true, "already": true, "key": ch.Key}, nil, nil
	}
	// A subagent already speaks here through its parent, and SPEC-CHANNELS.md
	// §8.2 is explicit that it must not join, queue or count separately.
	//
	// Letting it join was not merely redundant, it deadlocked: a subagent asking
	// to join the space its own PARENT holds exclusively was queued behind that
	// parent: position 2, with a hint telling it to send the owner a request,
	// and the parent does not release until the subagent's work is done. Each
	// waits for the other. Meanwhile post from that subagent already worked,
	// because speaking is what the inherited membership is for.
	if under := s.speaksFor(ch, l.ID); under != "" {
		return Result{
			"agent_id": ch.ID, "joined": true, "already": true, "under": under,
			"detail": "you are in this agent through " + under +
				", the agent that spawned you: subagents inherit agents rather than " +
				"joining them, so you can post and announce here already and do not " +
				"count as a separate member",
		}, nil, nil
	}

	// An exclusive space held by somebody else does not admit; it queues. That
	// is the difference between a refusal and coordination: a blocked agent
	// with a queue position has somewhere to be, one with a refusal has only
	// the option of ignoring it (SPEC-CHANNELS.md §5).
	if ch.Owner != "" && ch.Owner != l.ID {
		for i, q := range ch.Queue {
			if q == l.ID {
				return Result{
					"agent_id": ch.ID, "joined": false, "queued": true,
					"queue_position": i + 1, "owner": ch.Owner,
				}, nil, nil
			}
		}
		ch.Queue = append(ch.Queue, l.ID)
		if ch.Pending == nil {
			ch.Pending = map[string]*Membership{}
		}
		ch.Pending[l.ID] = memberFromOp(l.ID, op, 0) // serial filled in on promotion
		evs := []Event{{Type: "agent.queued", Agent: l.ID, To: ch.Owner, Data: map[string]any{
			"agent_id": ch.ID, "queue_position": len(ch.Queue), "owner": ch.Owner,
			"score": op.Score,
		}}}
		s.finish(&evs, now)
		return Result{
			"agent_id": ch.ID, "joined": false, "queued": true,
			"queue_position": len(ch.Queue), "owner": ch.Owner,
			"hint": "the space is exclusive; send its owner a request, or wait to be admitted",
		}, evs, nil
	}

	m := memberFromOp(l.ID, op, s.Serial+1)
	ch.Members[l.ID] = m
	ch.Predicted = mergePredicted(ch.Predicted, op.Predicted)
	delete(ch.Subs, l.ID) // membership supersedes subscription
	evs := []Event{{Type: "agent.joined", Agent: l.ID, Data: map[string]any{
		"agent_id": ch.ID, "auto": op.Auto, "score": op.Score,
		"threshold": op.Threshold, "scorer": m.ScorerID, "evidence": op.Evidence,
		"members": len(ch.Members),
	}}}
	s.finish(&evs, now)
	return Result{
		"agent_id": ch.ID, "joined": true, "members": len(ch.Members),
		"topic": ch.Topic, "auto": op.Auto, "score": op.Score, "key": ch.Key,
		"key_hint": "declare this in `refs` on later declare calls and Dibs will " +
			"match you to this agent exactly, instead of guessing from your wording",
	}, evs, nil
}

// ── leave ────────────────────────────────────────────────────────────────

func (s *State) applySpaceExclusive(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	ch := s.Spaces[cleanID(op.Space)]
	if ch == nil {
		return nil, nil, errf("E_NO_SPACE", "open_space or join_space it first", "no space %s", op.Space)
	}
	if _, ok := ch.Members[l.ID]; !ok {
		return nil, nil, errf("E_NOT_MEMBER", "join_space first", "not a member of %s", ch.ID)
	}
	release := op.Mode == "release" || op.Mode == "shared"
	if release {
		if ch.Owner != l.ID {
			return nil, nil, errf("E_NOT_OWNER", "only the owner may release", "%s is owned by %q", ch.ID, ch.Owner)
		}
		evs := s.releaseExclusive(ch, "released by its owner")
		s.finish(&evs, now)
		return Result{"agent_id": ch.ID, "exclusive": ch.Owner != ""}, evs, nil
	}
	if ch.Owner != "" && ch.Owner != l.ID {
		return nil, nil, errf("E_SPACE_EXCLUSIVE", "request access from the owner, or queue",
			"%s is already exclusive to %s", ch.ID, ch.Owner)
	}
	// Only the FIRST member may take an agent exclusively (§5). Letting the
	// fourth arrival lock out the three already working is not coordination.
	if len(ch.Members) > 1 && ch.Owner == "" {
		return nil, nil, errf("E_SPACE_SHARED", "ask the other members to leave, or coordinate in the space",
			"%s already has %d members; exclusivity is for the first", ch.ID, len(ch.Members))
	}
	ch.Owner = l.ID
	evs := []Event{{Type: "agent.exclusive", Agent: l.ID, Data: map[string]any{
		"agent_id": ch.ID, "owner": l.ID,
	}}}
	s.finish(&evs, now)
	return Result{"agent_id": ch.ID, "exclusive": true, "owner": l.ID}, evs, nil
}

func (s *State) releaseExclusive(ch *Space, why string) []Event {
	// Name WHO stopped owning and WHY, and carry SPEC §9's caution.
	//
	// This emitted a bare event with only the agent id: no former owner, no
	// cause, no caution: while the two neighbouring release paths carry all
	// three. It is also the path a liveness sweep uses, which is the one case
	// where the caution matters most: a consumer that cannot tell a deliberate
	// release from a lapsed lease can read "released" as safe-to-take.
	former := ch.Owner
	ch.Owner = ""
	evs := []Event{{Type: "agent.released", Agent: former, Data: map[string]any{
		"agent_id": ch.ID, "former_owner": former, "cause": why,
		"caution": "the owner's coordination signal ended; this is not proof its work stopped",
	}}}
	// Everyone waiting is admitted at once: the thing they were waiting for is
	// gone, and admitting them one at a time would just be a slower queue.
	for _, q := range ch.Queue {
		if s.Agents[q].Gone() {
			continue
		}
		ch.promote(q, s.Serial+1)
		evs = append(evs, Event{Type: "agent.joined", Agent: q, Data: map[string]any{
			"agent_id": ch.ID, "from_queue": true,
		}})
	}
	ch.Queue = nil
	return evs
}

// ── traffic ──────────────────────────────────────────────────────────────

func (s *State) applySpacePost(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	ch, err := s.memberChannel(l, op)
	if err != nil {
		return nil, nil, err
	}
	if len(op.Body) > s.Limits.MaxBodyBytes {
		return nil, nil, errTooLarge("post body", s.Limits.MaxBodyBytes)
	}
	// Attributed to the membership holder: a subagent's traffic is its parent's
	// traffic, so peers see one participant rather than a crowd (§8.2).
	speaker := s.speaksFor(ch, l.ID)
	// METADATA ONLY. SPEC §10 is unambiguous that events say WHAT happened and
	// never what was said, and bodies come from an authenticated read.
	//
	// This carried the whole post, and space events have no `To`, so
	// filterEvents delivered it to every authenticated agent on the board: a
	// non-member reading events_since received the text verbatim. The
	// member/subscriber/outsider distinction that watch_space exists to draw
	// was collapsed: everyone got the same bodies. Direct mail was always
	// correct here, which is why the existing test passed; it only ever sent
	// mail, never posted.
	//
	// The serial is what a reader needs. Members and subscribers fetch the text
	// with read_space, which checks who is asking.
	evs := []Event{{Type: "agent.post", Agent: speaker, Data: map[string]any{
		"agent_id": ch.ID, "from": l.ID, "on_behalf_of": speaker,
		"bytes":    len(op.Body),
		"audience": len(ch.Members) + len(ch.Subs),
	}}}
	serial := s.finish(&evs, now)
	ch.Posts = append(ch.Posts, Post{
		Serial: serial, From: l.ID, OnBehalfOf: speaker, Body: op.Body, At: now,
	})
	// Bounded like every other replayed collection, and by simple truncation
	// rather than a GC pass: a post carries no obligation, so the oldest is
	// always the one to drop and nothing has to be checked before dropping it.
	if excess := len(ch.Posts) - s.Limits.PostRetention; excess > 0 {
		ch.Posts = append([]Post(nil), ch.Posts[excess:]...)
	}
	return Result{"agent_id": ch.ID, "serial": serial}, evs, nil
}

func (s *State) applySpaceAnnounce(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	// The awareness gate (SPEC §6) applies here above all.
	//
	// An announcement is the strongest thing an agent can do to an agent: it
	// obliges every member to acknowledge it and re-pings them until they do.
	// Doing that without having read the board is exactly what the gate exists
	// to prevent: an agent that has just reattached after losing its context
	// could announce "FREEZE auth/token.go" while contradicting an announcement
	// made thirty seconds earlier by somebody else, and oblige the whole agent to
	// answer it.
	//
	// It was gated on join_space and open_space, so the WEAKER acts were checked
	// and the strongest was not. `post` is deliberately still ungated:
	// posting is traffic nobody must answer, and gating a remark is friction
	// without a corresponding risk.
	if l.AckedSerial == 0 {
		return nil, nil, ErrMustAck
	}
	ch, err := s.memberChannel(l, op)
	if err != nil {
		return nil, nil, err
	}
	if len(op.Body) > s.Limits.MaxBodyBytes {
		return nil, nil, errTooLarge("announce body", s.Limits.MaxBodyBytes)
	}
	speaker := s.speaksFor(ch, l.ID)
	req := map[string]bool{}
	for id := range ch.Members {
		if id != speaker {
			req[id] = true // nobody acknowledges their own news
		}
	}
	// Metadata only, for the same reason as agent.post above: an announcement is
	// the loudest thing on the board and its text still belongs to the agent.
	evs := []Event{{Type: "agent.announce", Agent: speaker, Data: map[string]any{
		"agent_id": ch.ID, "from": l.ID, "on_behalf_of": speaker,
		"bytes":    len(op.Body),
		"must_ack": len(req),
	}}}
	serial := s.finish(&evs, now)
	s.Announcements[serial] = &Announcement{
		Serial: serial, Space: ch.ID, From: speaker, Body: op.Body,
		Acked: map[string]bool{}, Required: req, MadeAt: evs[0].TS,
		State: stateFor(req),
	}
	return Result{"agent_id": ch.ID, "serial": serial, "must_ack": len(req)}, evs, nil
}

// An announcement nobody has to acknowledge is already settled; leaving it
// "open" would park it in the board's unresolved column forever.
func stateFor(req map[string]bool) string {
	if len(req) == 0 {
		return AnnounceAcked
	}
	return AnnounceOpen
}

func (s *State) applySpaceAck(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	a := s.Announcements[op.MsgSerial]
	if a == nil {
		return nil, nil, errf("E_NO_ANNOUNCE", "check the serial", "no announcement at serial %d", op.MsgSerial)
	}
	if !a.Required[l.ID] {
		return Result{"serial": a.Serial, "acked": false, "reason": "not required from you"}, nil, nil
	}
	if a.Acked[l.ID] {
		return Result{"serial": a.Serial, "acked": true, "already": true}, nil, nil
	}
	a.Acked[l.ID] = true
	outstanding := len(a.Required) - len(a.Acked)
	evs := []Event{{Type: "agent.acked", Agent: l.ID, To: a.From, Data: map[string]any{
		"agent_id": a.Space, "serial": a.Serial, "outstanding": outstanding,
	}}}
	if outstanding == 0 {
		a.State = AnnounceAcked
		evs = append(evs, Event{Type: "agent.announce_settled", Agent: a.From, Data: map[string]any{
			"agent_id": a.Space, "serial": a.Serial,
		}})
	}
	s.finish(&evs, now)
	return Result{"serial": a.Serial, "acked": true, "outstanding": outstanding}, evs, nil
}

// Unacked lists announcements still awaiting a given agent, oldest first.
//
// Sorted rather than map-ordered because this drives redelivery, and redelivery
// that changes order between two runs is redelivery that cannot be tested.
func (s *State) Unacked(agent string) []*Announcement {
	var out []*Announcement
	for _, a := range s.Announcements {
		if a.State == AnnounceOpen && a.Required[agent] && !a.Acked[agent] {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Serial < out[j].Serial })
	return out
}

// UnackedFor is what an agent still owes an acknowledgement on, ready to
// return from any read.
//
// Announcements used to reach an agent through exactly ONE path: the wake
// injection in hook_poll, which made them the only obligation in the system
// that could not be PULLED. An agent whose harness has no plugin installed
// never saw them at all; one that lost its context had no way to ask what it
// owed; and because redelivery is rate-limited and consumed on read, a digest
// that arrived at a bad moment was gone until the next retry window.
//
// A push-only obligation is a silent one. Both read paths return this: inbox,
// and check_in, which is the documented checkpoint after context loss and so
// is exactly where an agent that has forgotten everything comes looking.
// UnackedFor returns the announcements this agent still owes an acknowledgement
// on: an EMPTY slice when there are none, never nil.
//
// The distinction is not pedantry here. check_in is documented as the recovery
// checkpoint, and an omitted key left an agent that had just lost its context
// unable to tell "you missed nothing" from "this is not working" or "I am asking
// on the wrong agent". Reported as a defect by the first agent to reach for it
// that way, and it was right: a checkpoint has to answer, including with nothing.
func (s *State) UnackedFor(agent string) []Result {
	un := s.Unacked(agent)
	if len(un) == 0 {
		return []Result{}
	}
	out := make([]Result, 0, len(un))
	for _, a := range un {
		out = append(out, Result{
			"serial": a.Serial, "agent": a.Space, "from": a.From, "body": a.Body,
			"made_at": a.MadeAt, "retries": a.Retries,
			"action": "you must call ack_announcement with msg_serial " +
				strconv.FormatUint(a.Serial, 10) + " once you have read and accounted for this",
		})
	}
	return out
}

// SpaceHistory returns what has been ANNOUNCED in an agent, for a member.
//
// This did not exist, and its absence was found the way these things are found:
// a reviewing agent joined an agent, could see neither the announcement that had
// been made before it arrived nor any way to ask for it, and had to message a
// human to be told what the agent was about.
//
// Announcements bind their ack requirement to the members present when they are
// made, which is right: arriving late must not saddle you with an obligation
// for something said before you existed. But "you do not OWE this" was silently
// implemented as "you cannot SEE this", and those are different. An agent is
// supposed to be shared context; a newcomer got none of it.
//
// Sharpest form of the bug: the notice sent to an admitted agent reads "you may
// start; read the agent first": an instruction naming no tool that could do it.
//
// Members only. An announcement's body is for the agent, not for anyone who can
// name it, which is the same rule the wake path follows.
func (s *State) SpaceHistory(ch *Space, agent string, limit int) []Result {
	if limit <= 0 {
		limit = 50
	}
	var all []*Announcement
	for _, a := range s.Announcements {
		if a.Space == ch.ID {
			all = append(all, a)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Serial < all[j].Serial })
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	out := make([]Result, 0, len(all))
	for _, a := range all {
		r := Result{
			"serial": a.Serial, "from": a.From, "body": a.Body, "made_at": a.MadeAt,
		}
		// Say plainly whether this one is the reader's business to answer, so a
		// member cannot mistake context for an obligation or the reverse.
		switch {
		case !a.Required[agent]:
			r["your_ack"] = "not required: this was announced before you joined, or you sent it"
		case a.Acked[agent]:
			r["your_ack"] = "done"
		default:
			r["your_ack"] = "OWED: call ack_announcement with msg_serial " +
				strconv.FormatUint(a.Serial, 10)
		}
		out = append(out, r)
	}
	return out
}

// ── helpers ──────────────────────────────────────────────────────────────

// MemberChannel resolves an agent the caller is a member of, by name. The read
// path's counterpart to memberChannel, which takes an Op.
func (s *State) MemberChannel(l *Agent, name string) (*Space, error) {
	return s.memberChannel(l, &Op{Space: name})
}

func (s *State) memberChannel(l *Agent, op *Op) (*Space, error) {
	ch := s.Spaces[cleanID(op.Space)]
	if ch == nil {
		return nil, errf("E_NO_SPACE", "open_space or join_space first", "no space %s", op.Space)
	}
	if s.speaksFor(ch, l.ID) == "" {
		return nil, errf("E_NOT_MEMBER", "join_space first: subscribers read, members speak",
			"not a member of %s", ch.ID)
	}
	return ch, nil
}

// SpeaksFor exports speaksFor for callers that must distinguish a member (or a
// subagent acting under an ancestor's membership) from a mere subscriber.
func (s *State) SpeaksFor(ch *Space, id string) string { return s.speaksFor(ch, id) }

// speaksFor returns the membership an agent is acting under in a space:
// itself, or the ancestor whose membership it inherited. "" means neither.
//
// This is what makes spawning a subagent free (SPEC-CHANNELS.md §8.2). A
// subagent that had to join would be counted as a second occupant of its
// parent's work, which is not a collision (it is one agent's own helper) and
// on an exclusive space it would queue behind its own parent forever.
//
// Bounded walk: a parent chain is a tree in practice, but a corrupted or
// hand-edited ledger could contain a cycle, and an unbounded walk inside the
// single writer loop would hang the whole daemon rather than fail one call.
func (s *State) speaksFor(ch *Space, agent string) string {
	seen := map[string]bool{}
	for id := agent; id != "" && len(seen) < 16; {
		if _, ok := ch.Members[id]; ok {
			return id
		}
		if seen[id] {
			return "" // cycle
		}
		seen[id] = true
		l := s.Agents[id]
		if l == nil || !l.ParentProven {
			return "" // an unvouched lineage inherits nothing
		}
		id = l.Parent
	}
	return ""
}

// DescendsFrom reports whether agent is ancestor, or was spawned by it through
// any chain of subagents.
//
// Bounded and cycle-safe for the same reason speaksFor is: Parent is
// self-reported by the registering agent, so a malformed or hostile chain must
// terminate rather than hang the writer loop.
func (s *State) DescendsFrom(agent, ancestor string) bool {
	if agent == "" || ancestor == "" {
		return false
	}
	seen := map[string]bool{}
	for id := agent; id != "" && len(seen) < 16; {
		if id == ancestor {
			return true
		}
		if seen[id] {
			return false // cycle
		}
		seen[id] = true
		l := s.Agents[id]
		// An unvouched parent is a claim, not a fact, and this function decides
		// whether the guard waives somebody's exclusive claim. Walking an
		// unproven link would let any agent name a victim as its parent and
		// write inside that victim's claim.
		if l == nil || !l.ParentProven {
			return false
		}
		id = l.Parent
	}
	return false
}

// cleanID normalises a space id so "Auth Refactor" and "auth-refactor" are
// not two different agents.
func cleanID(s string) string {
	out := make([]rune, 0, len(s))
	lastDash := true // trims a leading dash
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
			lastDash = false
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
			lastDash = false
		case !lastDash:
			out = append(out, '-')
			lastDash = true
		}
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

// ── lifecycle integration ────────────────────────────────────────────────

// yieldChannelOwnership releases every agent this agent holds exclusively,
// without touching its membership.
//
// Called when an agent leaves `active`: the same moment its directory claims
// end, and for the same reason. Ownership is the part that BLOCKS other agents,
// so a fleet must never stay wedged behind an agent that crashed. Membership is
// merely informative, costs nobody anything, and a dormant persistent agent
// that wakes should still find itself in the agents it was working in.
//
// The honesty rule from SPEC §9 carries over unchanged and is stated in the
// event: the coordination signal ended, which is not proof the owner's work
// stopped or that the agent is safe to take.
func (s *State) yieldChannelOwnership(agent string) []Event {
	var evs []Event
	for _, id := range s.channelIDs() {
		ch := s.Spaces[id]
		if ch.Owner != agent {
			continue
		}
		evs = append(evs, s.releaseExclusive(ch, "the owner stopped coordinating")...)
	}
	return evs
}

// reclaimFinishedAgents deletes agents nobody is in and nobody owes anything to.
//
// Dibs had no way to end. `merge_spaces` was the only path that removed one, and
// E_AGENT_LIMIT told the operator to "close a finished agent first": naming a
// corrective action that did not exist, which is this codebase's most persistent
// failure mode in yet another place.
//
// It became urgent the moment declarations started opening agents automatically:
// before that, 64 was a generous ceiling on agents a human had chosen to create;
// after it, a fleet working through 64 unrelated tasks exhausts the cap for
// good, and every later declaration silently gets no agent.
//
// "Finished" is deliberately narrow. AUTO-OPENED, no members, nobody queued,
// and nothing outstanding. An agent that crashed still holds its membership
// until the sweep archives it, so an agent whose members are merely quiet is not
// touched.
//
// Auto-opened is the load-bearing one, and it follows from the paragraph above:
// the pressure on the cap comes from agents Dibs itself opens per declaration,
// never from the ones a human chose to create. Reclaiming both destroyed the
// standing agents that outliving your members is FOR: an agent returning to
// "release" found nothing, and the test protecting that property passed only
// because it never ran a sweep.
func (s *State) reclaimFinishedAgents() []Event {
	var evs []Event
	for _, id := range s.channelIDs() {
		ch := s.Spaces[id]
		if !ch.Auto || len(ch.Members) > 0 || len(ch.Queue) > 0 {
			continue
		}
		// An announcement that was never acknowledged is the record that
		// something went unanswered, and the board renders announcements THROUGH
		// their agent, so reclaiming the agent does not delete the record, it
		// hides it. The datum survives in s.Announcements and nothing can see it
		// again, which is the worse half of losing it.
		//
		// Both non-acked states count. `unacked` in particular is the one that
		// matters: it means redelivery gave up, which is exactly when somebody
		// needs to still be able to find it. Testing only for `open` reclaimed
		// those agents and took the evidence off the board.
		outstanding := false
		for _, a := range s.Announcements {
			if a.Space == id && a.State != AnnounceAcked {
				outstanding = true
				break
			}
		}
		if outstanding {
			continue
		}
		// The agent's announcements go with it.
		//
		// Reclaiming the space alone left them keyed by an agent id that no
		// longer existed, and SpaceHistory selects purely by that id, so the
		// moment anyone opened an agent with the same id (and ids are derived from
		// the declaration, so identical work reuses one), read_space handed a
		// stranger the previous agent's announcement bodies. Members-only content
		// to a non-member, surviving a restart.
		//
		// Two of my own changes combined to make this: automatic reclamation
		// created dead ids, and read_space gave them a reader. Neither was wrong
		// alone.
		//
		// Safe to delete because reclamation only happens when every
		// announcement here is ACKED: settled, everyone accounted for it. The
		// ledger keeps the full record either way; this is live state, not the
		// audit trail.
		for serial, a := range s.Announcements {
			if a.Space == id {
				delete(s.Announcements, serial)
			}
		}
		delete(s.Spaces, id)
		evs = append(evs, Event{Type: "agent.reclaimed", Data: map[string]any{
			"agent_id": id, "topic": ch.Topic,
			"why": "the last member left and nothing is outstanding",
		}})
	}
	return evs
}

// departAllChannels removes an agent from every agent, for close and archive.
func (s *State) departAllChannels(agent string) []Event {
	var evs []Event
	for _, id := range s.channelIDs() {
		ch := s.Spaces[id]
		if _, member := ch.Members[agent]; member {
			evs = append(evs, s.departChannel(ch, agent)...)
			continue
		}
		dequeue(ch, agent)
	}
	return append(evs, s.dropAckRequirements(agent)...)
}

// dequeue removes an agent from a space's waiting list.
func dequeue(ch *Space, agent string) {
	delete(ch.Pending, agent) // no longer waiting: nothing to promote it with
	for i, q := range ch.Queue {
		if q == agent {
			ch.Queue = append(ch.Queue[:i:i], ch.Queue[i+1:]...)
			return
		}
	}
}

// dropAckRequirements releases announcements waiting on an agent that is gone.
//
// An announcement can never be settled by an agent that no longer exists, so
// its requirement is dropped rather than left outstanding forever. Waiting on
// the dead is how an "unresolved" column fills with things nobody can act on,
// until people stop reading it.
// dropAckRequirementsIn releases what an agent owed a SINGLE agent, for when it
// leaves that agent rather than the board.
func (s *State) dropAckRequirementsIn(space, agent string) []Event {
	return s.releaseAcks(agent, func(an *Announcement) bool { return an.Space == space })
}

func (s *State) dropAckRequirements(agent string) []Event {
	return s.releaseAcks(agent, func(*Announcement) bool { return true })
}

// releaseAcks drops an agent's outstanding acknowledgements where want says so,
// recording that it left without reading rather than pretending it did.
func (s *State) releaseAcks(agent string, want func(*Announcement) bool) []Event {
	var evs []Event
	for _, serial := range s.announcementSerials() {
		an := s.Announcements[serial]
		if !an.Required[agent] || an.Acked[agent] || !want(an) {
			continue
		}
		delete(an.Required, agent)
		an.DepartedUnacked = append(an.DepartedUnacked, agent)
		if an.State != AnnounceOpen || len(an.Acked) < len(an.Required) {
			continue
		}
		// Nobody read it. `acked` would be a lie, and the honest terminal state
		// already exists: `unacked` means "this was never acknowledged", stays
		// on the board, and is the one mark that says a person should look. The
		// cause differs from a spent retry budget; the fact does not.
		an.State = AnnounceAcked
		if len(an.Acked) == 0 {
			an.State = AnnounceUnacked
		}
		evs = append(evs, Event{Type: "agent.announce_settled", Agent: an.From, Data: map[string]any{
			"agent_id": an.Space, "serial": an.Serial, "state": an.State,
			"cause":    "remaining members departed without acknowledging",
			"departed": append([]string(nil), an.DepartedUnacked...),
		}})
	}
	return evs
}

// channelIDs and announcementSerials give map iteration a fixed order.
//
// Not a style preference: these drive ledgered events, and events emitted in
// map order differ between two replays of the same ledger. That is exactly the
// class of non-determinism the hash chain exists to detect, so it must not be
// introduced by a range statement.
func (s *State) channelIDs() []string {
	out := make([]string, 0, len(s.Spaces))
	for id := range s.Spaces {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (s *State) announcementSerials() []uint64 {
	out := make([]uint64, 0, len(s.Announcements))
	for k := range s.Announcements {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ── matching ─────────────────────────────────────────────────────────────

// AgentMatch is one candidate SPACE for a piece of declared work.
//
// The field held a space id under the name `Agent` from the 0.0.3 rename until
// now, and it is where two rounds of damage came from rather than a cosmetic
// oddity: every sentence written about `m.Agent` came out saying agent when it
// meant space, so hints told agents to "read the agent with read_space" and a
// comment block explained closing an agent that was a space throughout. The
// engine's own Suggestion was corrected in f54d766; this, the thing it is built
// from, was not.
//
// Never serialised: MatchAgentsWith produces it, the engine consumes it and
// emits Suggestion, and no path marshals one to a client. So the tag moves with
// the identifier, which is only safe BECAUSE nothing reads it off the wire.
type AgentMatch struct {
	Space     string     `json:"space"`
	Topic     string     `json:"topic"`
	Score     float64    `json:"score"`
	Shared    []PredFile `json:"shared,omitempty"`
	Members   int        `json:"members"`
	Owner     string     `json:"owner,omitempty"`
	AlreadyIn bool       `json:"already_member,omitempty"`
	// Declined: this agent left this SPACE on purpose. Still surfaced, never
	// auto-joined. See Space.Declined.
	Declined bool `json:"declined,omitempty"`

	// SharedRefs are objective ids BOTH sides declared. "pr:1231", "gate:glossary",
	// "incident:solis-down". This is the difference between knowing and guessing.
	//
	// Score is inferred: a scorer's opinion about whether two pieces of work look
	// alike, and on a live fleet its false positives were confident and its recall
	// was 26%. A shared ref is not inferred at all. Two agents that both wrote
	// "pr:1231" are working on pr:1231, and no threshold is involved.
	//
	// Nothing consulted refs before this, which is the odd part: the most reliable
	// signal an agent gives was stored and ignored while the least reliable one
	// decided everything. Reported by an agent whose fleet had just had a genuine
	// three-way collision. "refs matching is the right primitive... that incident
	// is exactly what Dibs should have caught, and would have, on refs."
	SharedRefs []string `json:"shared_refs,omitempty"`

	// SharedIDs are the shared refs that NAME something, and they are the only
	// ones an automatic join may rest on. See identifyingRef.
	SharedIDs []string `json:"shared_ids,omitempty"`

	// Evidence is what this match actually rests on, slot-to-slot against the
	// member whose live declaration is closest, not against the agent's
	// accumulated union, which only grows. Relation is what Evidence.Classify
	// made of it, and it is what the decision reads.
	Evidence Evidence `json:"evidence,omitzero"`
	Relation Relation `json:"relation,omitempty"`
}

// evidenceAgainstMembers compares one live declaration against each member's own
// live declaration and keeps the strongest relation.
//
// Slot-to-slot, deliberately. Comparing against ch.Predicted: the union of
// everything every member has ever been predicted to touch: made an agent an
// easier target the longer it lived, because that union grows on every join and
// never shrinks. Measured: the same unrelated newcomer scored 0.0000 against a
// one-member agent and 0.1000 against the same agent with five.
//
// The union is still what generated this candidate, which is the job breadth is
// good for. It is not what decides.
// The third return says whether there was anything to judge AGAINST: at least
// one member holding a live declaration with a footprint. It is not a detail.
// "I compared you against this space's members and none resembles you" and "this
// space's members have declared nothing I could compare you to" are opposite
// facts, and scoring both as zero made every agent whose members had not yet
// called declare permanently invisible: including the ordinary case of an
// agent opening an agent for work it is about to start.
func (s *State) evidenceAgainstMembers(
	ch *Space, mine Slot, myCWD, repo string, discount map[string]float64, lens RepoLens,
) (Evidence, Relation, bool) {
	best, bestRel := Evidence{SameRepo: true}, RelationNone
	compared := false
	for agent := range ch.Members {
		l := s.Agents[agent]
		if l == nil {
			continue
		}
		theirCWD := ""
		if l.Agent != nil {
			theirCWD = l.Agent.CWD
		}
		for _, theirs := range l.Slots {
			// Their claims get the same scrutiny as the newcomer's; a key holds
			// or it does not, whichever side wrote it down.
			theirs.Refs = s.validatedRefs(agent, theirs.Refs)
			// A slot the scorer had no opinion about cannot testify to
			// dissimilarity either: it was never measured.
			compared = compared || len(theirs.Predicted) > 0
			ev := EvidenceBetween(mine, theirs, myCWD, theirCWD, repo, discount, lens)
			rel := ev.Classify()
			// Strongest relation wins; among equals, the closest declaration.
			// Ranking on relation alone left the reported evidence arbitrary among
			// several members sharing a relation, so an agent could be shown the
			// least similar of the peers it actually matched.
			if relationRank(rel) > relationRank(bestRel) ||
				(relationRank(rel) == relationRank(bestRel) && ev.Semantic > best.Semantic) {
				best, bestRel = ev, rel
			}
		}
	}
	return best, bestRel, compared
}

// judgedScore replaces a union-derived score with the closest live declaration's.
//
// The union FOUND this candidate; it does not get to judge it. ch.Predicted is
// every member's footprint merged, and merging is monotonic: it grows on each
// join and never shrinks, so an agent became an easier target the longer it lived,
// and an agent that matched more gained members and gained surface by gaining them.
// Measured before this changed: the same unrelated newcomer scored 0.0000 against
// a one-member agent and 0.1000 against the same agent with five, crossing a real
// fleet's join bar with no change to its work and none to the agent's topic.
//
// Breadth is the right property for FINDING candidates and the wrong one for
// judging them. The score now comes from the closest single live declaration,
// which is the thing an agent can actually be duplicating.
// There is deliberately no fallback to unionScore. Keeping one meant that an agent
// where NO member matched still scored on the merged footprint, which is the
// accretion bug itself, surviving in the one branch that looked harmless. If no
// live declaration in the agent resembles this one, the honest score is zero and
// `worthless` drops the candidate.
func judgedScore(union float64, ev Evidence, compared bool) float64 {
	if !compared {
		// Nothing was measured, so there is no verdict to prefer over the union.
		// Scoring this zero does not express doubt: it deletes the agent from
		// every future match, silently and permanently, and an agent opened for work
		// that has not been declared yet is the commonest shape there is: the
		// space e2e opens one on exactly that path and it vanished.
		//
		// The union may still overstate an agent that many agents joined. That is a
		// worse estimate; invisibility is not an estimate at all.
		return union
	}
	return ev.Semantic
}

// occupied reports whether anybody is actually in an agent.
//
// An agent nobody is in cannot be evidence that somebody else is doing your work,
// because there is no somebody. Matching exists to stop two AGENTS duplicating
// each other, so "another agent is already pursuing the same objective" is simply
// false about an empty agent, and "join it to coordinate" sends an agent to talk
// to an empty room. Found on a live board, not in a suite: two agents whose
// members had all been swept were still being offered, with `members=0` printed
// in the suggestion itself.
//
// Empty agents are not a transient state to wait out. An agent a human opened on
// purpose outlives its members deliberately, and only auto-opened ones are ever
// reclaimed, so they stay findable by name and joinable on purpose. What they
// stop doing is claiming to be occupied.
//
// A queue counts: an agent waiting on an exclusive space has not got in yet, but
// it is certainly working on that agent's subject.
func occupied(ch *Space) bool {
	return len(ch.Members) > 0 || len(ch.Queue) > 0
}

// worthless reports a candidate with nothing behind it at all.
func worthless(score float64, sharedRefs []string, rel Relation) bool {
	return score <= 0 && len(sharedRefs) == 0 && rel == RelationNone
}

// relationRank orders relations by strength so the strongest member match wins.
func relationRank(r Relation) int {
	switch r {
	case RelationSameItem:
		return 3
	case RelationContended:
		return 2 // a hard failure, ranked with surface: real, and not a duplicate
	case RelationSameSurface:
		return 2
	case RelationPossible:
		return 1
	case RelationNone:
		return 0
	}
	return 0
}

// identifyingRef reports whether a ref names a THING that exists, rather than an
// intention two agents can hold independently.
//
// The distinction is load-bearing and the first version of the join rule did not
// make it. Telling agents to always pass refs raises the fill rate by producing
// invented values, not identity, and the live fleet had already done exactly
// that: two agents that had deliberately partitioned the repository between them
// both declared goal:green-main, because both wanted main green. Under a rule
// that treats every ref as an identifier, they auto-join.
//
//	pr:1231, issue:88, incident:farm-down, cve:…, commit:…  name a thing
//	key:9f3c…                                               names a DECISION
//	goal:…, gate:…, area:…, epic:…                          name a wish
//
// `key` is Dibs' own coordination key and the only entry here Dibs can verify
// see coordkey.go. It arrives already validated: every path that reaches this
// function passes refs through State.validatedRefs first, which strikes out a
// key the declaring agent does not hold. `agent` used to sit in this list and no
// longer does. An agent id is a string an agent can write for an agent it merely
// believes it belongs in, and treating it as identity turned a belief into a
// verified fact: the exact laundering the opaque key exists to prevent.
//
// Unknown namespaces are treated as labels, which is the safe direction: an
// unrecognised ref surfaces the agent and lets the agent decide, rather than
// committing it on a word Dibs does not understand.
func identifyingRef(ref string) bool {
	ns, _, ok := strings.Cut(ref, ":")
	if !ok || ns == "" {
		return false
	}
	switch strings.ToLower(ns) {
	case "pr", "mr", "issue", "ticket", "incident", "cve", "commit", "bug", "task", coordKeyNS:
		return true
	}
	return false
}

// MatchAgentsWith is MatchAgents with a footprint overlay for agents whose own is
// empty: those opened before a scorer finished indexing. Still pure: the
// overlay is computed at the edge and handed in, exactly like the score.
func (s *State) MatchAgentsWith(agent string, pred []PredFile, overlay map[string][]PredFile, limit int) []AgentMatch {
	return s.MatchAgentsRefs(agent, pred, nil, overlay, limit)
}

// MatchAgentsRefs is MatchAgentsWith plus the objective ids the declaring agent
// gave, so a match can report DECLARED overlap alongside the inferred score. See
// AgentMatch.SharedRefs.
func (s *State) MatchAgentsRefs(
	agent string, pred []PredFile, refs []string, overlay map[string][]PredFile, limit int,
) []AgentMatch {
	return s.MatchAgentsEvidence(agent, Slot{Refs: refs, Predicted: pred}, "", "", nil, overlay, limit)
}

// MatchAgentsEvidence is the full form: it carries the declaring SLOT and the
// agent's location, so every match can report typed evidence computed against
// each member's own live declaration rather than the agent's accumulated union.
func (s *State) MatchAgentsEvidence(
	agent string, mine Slot, myCWD, repo string, lens RepoLens,
	overlay map[string][]PredFile, limit int,
) []AgentMatch {
	// A coordination key the declaring agent does not hold is struck out before
	// anything is compared, here rather than at ingress: the declaration is
	// stored exactly as the agent wrote it, and only what Dibs will ACT on is
	// filtered. Rejecting it in the fold instead would make an old ledger stop
	// replaying, and refusing the whole declare would let one bad ref lose a
	// declaration Dibs exists to hear.
	mine.Refs = s.validatedRefs(agent, mine.Refs)
	pred, refs := mine.Predicted, mine.Refs
	mineRefs := map[string]bool{}
	for _, r := range refs {
		if r != "" {
			mineRefs[r] = true
		}
	}
	// Declared facts are matched even when the scorer produced no footprint at
	// all: "no opinion" from a scorer must not suppress something both agents
	// wrote down by hand. Only a declaration with NOTHING in it is unmatchable.
	if len(pred) == 0 && len(mineRefs) == 0 && len(mine.Dirs) == 0 && len(mine.Holds) == 0 {
		return nil
	}
	discount := s.ubiquityDiscount(overlay)
	var out []AgentMatch
	for _, id := range s.channelIDs() {
		ch := s.Spaces[id]
		if !occupied(ch) {
			continue
		}
		fp := ch.Predicted
		if len(fp) == 0 {
			fp = overlay[id]
		}
		sharedRefs := s.sharedRefsWith(ch, mineRefs)
		if unmatchable(fp, sharedRefs) {
			continue
		}
		score, shared := jaccard(pred, fp, discount)
		ev, rel, compared := s.evidenceAgainstMembers(ch, mine, myCWD, repo, discount, lens)
		score = judgedScore(score, ev, compared)
		if worthless(score, sharedRefs, rel) {
			continue
		}
		_, in := ch.Members[agent]
		out = append(out, AgentMatch{
			Space: ch.ID, Topic: ch.Topic, Score: score, Shared: shared,
			Members: len(ch.Members), Owner: ch.Owner, AlreadyIn: in,
			Declined: ch.Declined[agent], SharedRefs: sharedRefs,
			SharedIDs: identifying(sharedRefs),
			Evidence:  ev, Relation: rel,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Space < out[j].Space // deterministic ties
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ubiquityDiscount measures how much evidence each file is actually worth here.
//
// A file that appears in almost every agent's footprint cannot distinguish
// between them. Justfile, .github/workflows/ci.yml, CMakeLists.txt, llms-full.txt
// every project has them, they co-change with everything, and so they turn up
// as "shared" between any two agents who happen to work in the same repository.
//
// Two agents reported this within an hour of each other and neither was wrong.
// One was auto-joined to an agent on evidence that was four-fifths repo-root files
// it had never declared and never written: "runtime/CMakeLists.txt,
// llms-full.txt, .github/workflows/ci.yml, Justfile". Its own summary was that
// they "match mainly because we are both in the same repo".
//
// This is inverse document frequency, and it is measured rather than listed on
// purpose. A hardcoded set of boring filenames would be wrong in the next
// repository, would need maintaining forever, and would still miss the
// project-specific file that everybody touches. Commonality is the property that
// actually matters, and the board already knows it.
//
// A file in one agent keeps its full weight. A file in every agent keeps almost
// none. Nothing is excluded outright: a genuine collision on a shared build file
// is real, it just should not outweigh two agents in the same package.
func (s *State) ubiquityDiscount(overlay map[string][]PredFile) map[string]float64 {
	ids := s.channelIDs()
	if len(ids) < 2 {
		return nil // nothing to compare against; every file is equally informative
	}
	docs := 0
	freq := make(map[string]int)
	for _, id := range ids {
		fp := s.Spaces[id].Predicted
		if len(fp) == 0 {
			fp = overlay[id]
		}
		if len(fp) == 0 {
			continue
		}
		docs++
		seen := make(map[string]bool, len(fp))
		for _, f := range fp {
			if seen[f.Path] {
				continue
			}
			seen[f.Path] = true
			freq[f.Path]++
		}
	}
	if docs < 2 {
		return nil
	}
	out := make(map[string]float64, len(freq))
	for p, n := range freq {
		// Linear in the share of agents that contain it: present in one agent of
		// many → ~1, present in all → ~0. Floored so evidence is discounted, never
		// erased: a file every agent touches is weak evidence, not counter-evidence.
		d := 1 - float64(n-1)/float64(docs)
		if d < 0.1 {
			d = 0.1
		}
		out[p] = d
	}
	return out
}

// jaccard is the weighted overlap of two footprints, plus the shared files that
// justify it. Weighting matters: unweighted, two agents who both barely touch
// go.mod score the same as two rewriting the same package.
//
// discount may be nil, in which case every file counts at face value. See
// ubiquityDiscount for why it usually should not be.
func jaccard(a, b []PredFile, discount map[string]float64) (float64, []PredFile) {
	w8 := func(path string, w float64) float64 {
		if d, ok := discount[path]; ok {
			return w * d
		}
		return w
	}
	aw := make(map[string]float64, len(a))
	for _, f := range a {
		if w := w8(f.Path, f.Weight); w > aw[f.Path] {
			aw[f.Path] = w
		}
	}
	var shared, total float64
	var ev []PredFile
	seen := make(map[string]bool, len(b))
	for _, f := range b {
		if seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		fw := w8(f.Path, f.Weight)
		w, ok := aw[f.Path]
		if !ok {
			total += fw
			continue
		}
		lo, hi := w, fw
		if lo > hi {
			lo, hi = hi, lo
		}
		shared += lo
		total += hi
		ev = append(ev, PredFile{Path: f.Path, Weight: lo})
	}
	for p, w := range aw {
		if !seen[p] {
			total += w
		}
	}
	if total == 0 {
		return 0, nil
	}
	sort.Slice(ev, func(i, j int) bool {
		if ev[i].Weight != ev[j].Weight {
			return ev[i].Weight > ev[j].Weight
		}
		return ev[i].Path < ev[j].Path
	})
	if len(ev) > 5 {
		ev = ev[:5]
	}
	return shared / total, ev
}

// ── the director: coordinator powers over spaces (SPEC-CHANNELS.md §8.1) ──
//
// The spec calls this role "director". It is NOT a new role: it is the existing
// `coordinator` (SPEC §5), scoped to spaces. Inventing a second privileged
// role would mean a second grant path to get right, and the existing one already
// has the property that matters: a human grants it, and no agent can promote
// itself (applyGrantRole is admitted only on the daemon's admin path).
//
// Every power here is one an agent owner already has over its own agent. The
// director's addition is being able to use them on somebody else's, which is
// what unsticks a fleet whose owner crashed, and every one of them is
// ledgered and announced, never silent.

func (s *State) directorOf(l *Agent, op *Op) (*Space, error) {
	if !l.IsCoordinator() {
		return nil, errf("E_NOT_COORDINATOR",
			"ask for it rather than waiting to be given it: send(to: the board row marked "+
				"`human: true`, type: \"request\", body: what you need it for) reaches the "+
				"person as a notification with Approve on it",
			"only a coordinator may administer another agent's space")
	}
	ch := s.Spaces[cleanID(op.Space)]
	if ch == nil {
		return nil, errf("E_NO_SPACE", "check the space id on the board", "no space %s", op.Space)
	}
	return ch, nil
}

// applySpaceForceRelease strips exclusivity from an agent the caller does not own.
//
// For the case the queue cannot solve on its own: an owner whose agent is gone
// but whose agent is still locked. The holder is named in the event, so this is
// never silent: same rule as force_release on a directory claim (SPEC §9).
func (s *State) applySpaceForceRelease(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	ch, err := s.directorOf(l, op)
	if err != nil {
		return nil, nil, err
	}
	if ch.Owner == "" {
		return Result{"agent_id": ch.ID, "released": false, "reason": "not exclusive"}, nil, nil
	}
	former := ch.Owner
	evs := []Event{{Type: "agent.force_released", Agent: l.ID, To: former, Data: map[string]any{
		"agent_id": ch.ID, "former_owner": former, "by": l.ID, "note": op.Note,
		"caution": "a coordinator released this; the former owner may still be working: verify",
	}}}
	evs = append(evs, s.releaseExclusive(ch, "forced by a coordinator")...)
	s.finish(&evs, now)
	return Result{"agent_id": ch.ID, "released": true, "former_owner": former}, evs, nil
}

// applySpaceEvict removes an agent from an agent it should not be in.
//
// This is also how "move an agent between agents" works: evict, and the agent
// joins the right one. A single move op would have to guess which agent is right,
// and getting that wrong silently relocates somebody's work.
func (s *State) applySpaceEvict(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	ch, err := s.directorOf(l, op)
	if err != nil {
		return nil, nil, err
	}
	target := op.To
	if target == "" {
		return nil, nil, errf("E_BAD_ARG", "name the agent to remove", "`to` is required")
	}
	// A QUEUED agent is not a member, and answering "not a member" is both
	// technically true and actively misleading: the director concludes the agent
	// is not on the agent and moves on, and the moment the owner leaves, the agent
	// it just tried to remove is promoted to OWNER of that agent. Observed
	// exactly: evict(waiter) returned evicted:false, then evict(owner) left
	// owner="waiter".
	//
	// Removing somebody from an agent has to mean removing them, so eviction takes
	// them out of the queue as well.
	if _, isMember := ch.Members[target]; !isMember {
		if i := slices.Index(ch.Queue, target); i >= 0 {
			ch.Queue = slices.Delete(ch.Queue, i, i+1)
			evs := []Event{{Type: "agent.evicted", Agent: target, To: target, Data: map[string]any{
				"agent_id": ch.ID, "by": l.ID, "note": op.Note, "from_queue": true,
				"detail": "a coordinator removed you from this agent's queue; you are no " +
					"longer waiting for it and will not be admitted",
			}}}
			s.finish(&evs, now)
			return Result{"agent_id": ch.ID, "evicted": true, "agent": target, "from_queue": true}, evs, nil
		}
		return Result{
			"agent_id": ch.ID, "evicted": false, "reason": "not a member",
			"detail": "nobody by that name is in this agent or waiting for it; check " +
				"the agent id against the board",
		}, nil, nil
	}
	evs := []Event{{Type: "agent.evicted", Agent: target, To: target, Data: map[string]any{
		"agent_id": ch.ID, "by": l.ID, "note": op.Note,
		"detail": "a coordinator removed you from this agent; your work is untouched",
	}}}
	evs = append(evs, s.departChannel(ch, target)...)
	s.finish(&evs, now)
	return Result{"agent_id": ch.ID, "evicted": true, "agent": target}, evs, nil
}

// applySpaceMerge folds one agent into another when they drifted into the same
// work: the case SPEC-CHANNELS.md §11 leaves open, resolved by a human-granted
// coordinator rather than by a score, because merging is destructive to context
// and a threshold is the wrong thing to trust with it.
// carryQueue moves the source agent's waiters somewhere real.
//
// Dropping them is the one thing that must not happen, and it is what used to
// happen: src.Queue=[waiter] became dst.Queue=[] and the waiter belonged to
// neither agent: blocked forever behind an owner that no longer existed.
//
// Where they go depends on the destination, and both answers give the agent
// what it was waiting for. If dst is exclusive they are still blocked, so they
// keep waiting, but on an agent that exists, in a queue they can be promoted out
// of. If dst is open, the thing they were waiting for is gone, so waiting is
// over and they are admitted.
func (s *State) carryQueue(src, dst *Space) (queued, admitted int) {
	for _, id := range src.Queue {
		if _, already := dst.Members[id]; already {
			continue
		}
		if s.Agents[id].Gone() {
			continue // the waiter is gone for good; nothing to carry
		}
		if dst.Owner != "" {
			if carryWaiter(src, dst, id) {
				queued++
			}
			continue
		}
		// Carried across with whatever it was queued on the SOURCE agent with:
		// a merge changes which agent the work lives in, not why the agent
		// belongs in it.
		if m := src.Pending[id]; m != nil {
			m.JoinedSerial = s.Serial + 1
			dst.Members[id] = m
			// The same rule as promote, and it has to be repeated here because
			// this is a second door into membership. A queued agent's footprint
			// is deliberately excluded from its own agent's Predicted: it is not
			// a member yet, so merging src.Predicted into dst does not carry
			// it. Without this, an agent that walks in through a merge instead
			// of a promotion is a full member whose files the agent has no
			// record of, which is exactly the hole the promote fix closed.
			dst.Predicted = mergePredicted(dst.Predicted, m.Predicted)
		} else {
			dst.Members[id] = &Membership{Agent: id, ScorerID: "merge", JoinedSerial: s.Serial + 1}
		}
		admitted++
	}
	return queued, admitted
}

// carryWaiter moves one still-blocked agent to the destination's queue, with
// the provenance it was queued under: a merge changes which agent the work lives
// in, not why the agent belongs in it.
func carryWaiter(src, dst *Space, id string) bool {
	if slices.Contains(dst.Queue, id) {
		return false
	}
	dst.Queue = append(dst.Queue, id)
	if m := src.Pending[id]; m != nil {
		if dst.Pending == nil {
			dst.Pending = map[string]*Membership{}
		}
		dst.Pending[id] = m
	}
	return true
}

// carryAnnouncements repoints outstanding traffic at the surviving agent.
//
// Left naming the deleted agent they are countable on NO board: invisible on
// the source because it is gone, invisible on the destination because they name
// the wrong id: while still obliging their members to acknowledge them. That
// is the abandoned-announcement failure mode exactly.
func (s *State) carryAnnouncements(src, dst *Space) {
	for _, ser := range s.announcementSerials() {
		if an := s.Announcements[ser]; an.Space == src.ID {
			an.Space = dst.ID
		}
	}
}

// mergeNotices tells everyone the merge moved.
//
// Their old agent is GONE, so an agent not told keeps addressing an agent that no
// longer exists and every call fails for a reason it cannot guess. Same
// category as an admission or an eviction. Three groups, and the ones still
// queued matter most, because nothing else in their world would ever change.
func mergeNotices(src, dst *Space, by string, wasHere []string) []Event {
	var evs []Event
	// The DESTINATION's people are affected too, and were told nothing.
	//
	// Their agent silently gains another space's members, its predicted
	// footprint, and its outstanding announcements, which they may now be
	// required to acknowledge. Only the moved side was woken, so the
	// destination's owner could carry on believing its agent was unchanged and
	// still exclusively its own while a whole other agent had been folded in.
	//
	for _, id := range wasHere {
		if id == by {
			continue // the coordinator did this; its own tool result said so
		}
		evs = append(evs, Event{Type: "agent.absorbed", Agent: id, Data: map[string]any{
			"agent_id": dst.ID, "merged_from": src.ID, "merged_by": by,
			"gained": len(src.Members),
		}})
	}
	base := func() map[string]any {
		return map[string]any{
			"agent_id": dst.ID, "merged_from": src.ID, "merged_by": by,
			"members": len(dst.Members),
		}
	}
	for _, id := range sortedKeys(src.Members) {
		if _, in := dst.Members[id]; in && id != by {
			evs = append(evs, Event{Type: "agent.joined", Agent: id, Data: base()})
		}
	}
	for _, id := range src.Queue {
		if _, in := dst.Members[id]; in {
			d := base()
			d["from_queue"] = true
			evs = append(evs, Event{Type: "agent.joined", Agent: id, Data: d})
			continue
		}
		if pos := slices.Index(dst.Queue, id); pos >= 0 {
			d := base()
			d["queue_position"], d["owner"] = pos+1, dst.Owner
			evs = append(evs, Event{Type: "agent.requeued", Agent: id, Data: d})
		}
	}
	return evs
}

// applySpaceClose retires a finished agent that nothing will ever reclaim.
//
// Auto-opened agents end by themselves: reclaimFinishedAgents deletes them once
// the last member leaves. Dibs a human opened deliberately do NOT, and that is
// correct: outliving your members is what a standing agent is FOR. The gap was
// that nothing could end one either, ever, by any path. A board accumulated
// finished agents permanently, and E_AGENT_LIMIT advised "leave_space the ones you
// are done with", which does nothing for exactly these: naming a corrective
// action that does not work is this codebase's most persistent failure mode, and
// this was another instance of it.
//
// A coordinator can now say so explicitly. That fits the director rule: every
// director power is one an agent owner already has over its own agent: with the
// honest caveat that for THIS one no owner had it either. It is the same
// decision reclaimFinishedAgents makes automatically, made on purpose by somebody
// accountable, and ledgered under their name.
//
// Deliberately refuses an OCCUPIED space rather than emptying it. Closing a space
// with members in it would evict them as a side effect of tidying up, and a
// coordinator that wants that has evict, which says what it does. Same for
// an unacknowledged announcement: it is the record that something went
// unanswered, and the board renders announcements through their space, so closing
// over one hides evidence rather than settling it.
func (s *State) applySpaceClose(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	ch := s.Spaces[cleanID(op.Space)]
	if ch == nil {
		return nil, nil, errf("E_NO_SPACE", "check the space id on the board", "no space %s", op.Space)
	}
	// The agent that OPENED a space may retire it, without the coordinator role.
	//
	// open_space is unprivileged and advertised, so an agent could create a space
	// and then never end it, and the refusal it got said "only a coordinator may
	// administer ANOTHER AGENT'S space" about its own. Telling somebody they may
	// not touch their own thing, in words describing somebody else's, is worse
	// than the missing power.
	//
	// Narrower than directorOf on purpose, and placed here rather than in it:
	// closing your own finished space is not the same act as merging your space
	// into a stranger's, and directorOf gates both. Every other guard below still
	// applies: an opener cannot close a space somebody is in, or one holding an
	// unanswered announcement, any more than a coordinator can.
	if ch.OpenedBy != l.ID {
		if _, err := s.directorOf(l, op); err != nil {
			return nil, nil, err
		}
	}
	// The sole member closing its own space is not eviction.
	//
	// The rule exists so nobody tidies away somebody else's working context, and
	// when the only occupant IS the caller there is no somebody else. Reported
	// by k7-b from a live board: close_space refused because the space had one
	// member, which was them; leave_space then removed the empty space outright,
	// so the close they had been told to make failed with E_NO_AGENT. The
	// documented path ended in an error and the working path was undocumented.
	soleOccupant := len(ch.Queue) == 0 && len(ch.Members) == 1 && ch.Members[l.ID] != nil
	if occupied(ch) && !soleOccupant {
		return nil, nil, errf("E_SPACE_OCCUPIED",
			"evict the members first, or leave it: a space with agents in it is "+
				"somebody's working context, not clutter",
			"space %s still has %d member(s) and %d queued",
			ch.ID, len(ch.Members), len(ch.Queue))
	}
	for _, a := range s.Announcements {
		if a.Space == ch.ID && a.State != AnnounceAcked {
			return nil, nil, errf("E_ANNOUNCE_OUTSTANDING",
				"the board shows announcements through their agent, so closing this one "+
					"would hide an unanswered obligation rather than settle it",
				"agent %s has an announcement nobody has acknowledged", ch.ID)
		}
	}
	// Its announcements go with it, for the reason reclaimFinishedAgents gives:
	// leaving them keyed by a dead id hands the next agent with that id a
	// stranger's history. Safe because every one of them is settled.
	for serial, a := range s.Announcements {
		if a.Space == ch.ID {
			delete(s.Announcements, serial)
		}
	}
	// The audit line describes what ACTUALLY happened.
	//
	// It was one fixed sentence, "closed by a coordinator; it was empty", and
	// since an opener may now close a space it still occupies alone, that
	// sentence became false about both the authority used and the occupancy. An
	// audit trail that is wrong about who did a thing and why is worse than one
	// that says less. Found by a pre-release review; the test for the new path
	// proved the close was allowed and never read the event it wrote.
	why := "closed by a coordinator; it was empty and everything in it was settled"
	if ch.OpenedBy == l.ID {
		why = "closed by the agent that opened it"
		if soleOccupant {
			why += ", which was also its only member"
		}
	}
	delete(s.Spaces, ch.ID)
	evs := []Event{{Type: "agent.closed", Agent: l.ID, Data: map[string]any{
		"agent_id": ch.ID, "topic": ch.Topic, "by": l.ID, "note": op.Note,
		"why": why,
	}}}
	s.finish(&evs, now)
	return Result{"agent_id": ch.ID, "closed": true, "topic": ch.Topic}, evs, nil
}

func (s *State) applySpaceMerge(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	src, err := s.directorOf(l, op)
	if err != nil {
		return nil, nil, err
	}
	dst := s.Spaces[cleanID(op.To)]
	if dst == nil {
		return nil, nil, errf("E_NO_SPACE", "check the destination space id", "no space %s", op.To)
	}
	if dst.ID == src.ID {
		return nil, nil, errf("E_BAD_ARG", "name two different spaces", "cannot merge %s into itself", src.ID)
	}
	// Who was already in the destination, BEFORE anything moves. Taken here
	// because mergeNotices runs after the merge and could not otherwise tell
	// the people who were already there from the ones who just arrived: the
	// arrivals get their own notice, and two notices for one event is how a
	// wake space becomes noise.
	wasHere := sortedKeys(dst.Members)

	// Exclusivity does not survive a merge: the destination may already have
	// members, and silently locking them out of their own agent would be a
	// surprise nobody asked for.
	moved := 0
	for _, id := range sortedKeys(src.Members) {
		if _, already := dst.Members[id]; already {
			continue
		}
		m := *src.Members[id]
		m.JoinedSerial = s.Serial + 1
		dst.Members[id] = &m
		moved++
	}
	dst.Predicted = mergePredicted(dst.Predicted, src.Predicted)
	for id := range src.Subs {
		if _, isMember := dst.Members[id]; !isMember {
			dst.Subs[id] = true
		}
	}

	queued, admitted := s.carryQueue(src, dst)
	s.carryAnnouncements(src, dst)
	s.carryPosts(src, dst)

	delete(s.Spaces, src.ID)
	evs := []Event{{Type: "agent.merged", Agent: l.ID, Data: map[string]any{
		"from": src.ID, "into": dst.ID, "moved": moved, "by": l.ID, "note": op.Note,
		"queued": queued, "admitted": admitted,
		"detail": "these two agents were the same work; read " + dst.ID + " for the full picture",
	}}}
	evs = append(evs, mergeNotices(src, dst, l.ID, wasHere)...)
	s.finish(&evs, now)
	return Result{
		"from": src.ID, "into": dst.ID, "moved": moved,
		"queued": queued, "admitted": admitted, "members": len(dst.Members),
	}, evs, nil
}

// applySpaceAdmit adds another agent to an agent, on a coordinator's authority.
//
// This is the approval half of `director_required` (SPEC-CHANNELS.md §8.1): with
// the gate on, scoring stops auto-joining and an agent waits to be admitted, so
// there has to be an act that admits it. It is also useful with the gate off,
// pulling somebody into work they should be part of but did not match.
//
// The recorded score travels through unchanged when the director is acting on a
// match, so a gated join is exactly as explainable as an automatic one; §10.3
// does not weaken because a human-granted role was in the loop.
func (s *State) applySpaceAdmit(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	ch, err := s.directorOf(l, op)
	if err != nil {
		return nil, nil, err
	}
	target := op.To
	if target == "" {
		return nil, nil, errf("E_BAD_ARG", "name the agent to admit", "`to` is required")
	}
	t := s.Agents[target]
	if t == nil || t.Status == StatusClosed || t.Status == StatusArchived {
		return nil, nil, errf("E_NO_AGENT", "check the board for live agents", "no live agent %q", target)
	}
	if _, already := ch.Members[target]; already {
		return Result{"agent_id": ch.ID, "admitted": true, "already": true}, nil, nil
	}
	ch.Members[target] = &Membership{
		Agent: target, Score: op.Score, Threshold: op.Threshold,
		ScorerID: firstNonEmpty(op.ScorerID, "director"), ScorerVersion: op.ScorerVersion,
		Evidence: op.Evidence, JoinedSerial: s.Serial + 1,
	}
	ch.Predicted = mergePredicted(ch.Predicted, op.Predicted)
	delete(ch.Subs, target)
	dequeue(ch, target)
	evs := []Event{{Type: "agent.joined", Agent: target, To: target, Data: map[string]any{
		"agent_id": ch.ID, "admitted_by": l.ID, "members": len(ch.Members),
		"score": op.Score, "note": op.Note,
	}}}
	s.finish(&evs, now)
	return Result{
		"agent_id": ch.ID, "admitted": true, "agent": target,
		"members": len(ch.Members),
	}, evs, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
