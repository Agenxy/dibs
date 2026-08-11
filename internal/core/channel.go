package core

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Channels: the semantic half of coordination. See SPEC-CHANNELS.md.
//
// NAMING, because it will otherwise confuse everyone who reads this file:
// SPEC-CHANNELS.md §1 renames the participant to "agent" and gives the word
// "lane" to the channel. The PROTOCOL uses that vocabulary already: the tools
// are `lane_join`, `lane_announce`: but the Go identifier for a participant is
// still `Lane`, because renaming it touches 15 files and 7 wire-format JSON
// tags, and that is a mechanical pass of its own rather than something to smuggle
// into a new subsystem. So in this file: `Channel` is a lane, and `Lane` is an
// agent. The wire names are the ones that must never drift, and they are right.
//
// What a channel adds over directory claims: claims detect two agents naming the
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

// Channel is a topic of work that agents join.
type Channel struct {
	ID    string
	Topic string
	// Auto records that LANES opened this lane from a declaration, rather than an
	// agent or a human opening it on purpose.
	//
	// It decides whether the lane can be reclaimed when it empties, and the two
	// answers are both right for their own case. A standing lane. "release",
	// "security review": outlives its members by design: agents drop in and out
	// and must find the same lane with its history. A lane opened automatically
	// for one declaration has no such claim, and reclaiming those is what keeps a
	// fleet from exhausting MaxLanes on work that finished hours ago.
	//
	// Without the distinction the codebase believed both at once: one test
	// asserted an empty lane persists, reclaimFinishedLanes deleted exactly those
	// lanes, and the test passed only because it never swept. A standing lane
	// really did disappear at the next tick.
	Auto bool
	// Key is the lane's coordination key: Lanes' own record that the agents in
	// here decided to work together. See coordkey.go: it is issued at open,
	// held by membership, and is the one identity claim Lanes can actually
	// verify.
	Key     string
	Members map[string]*Membership // agent id → how and why they joined
	Subs    map[string]bool        // subscribers: see traffic, never collide
	// Posts is the lane's remark history, newest last, bounded by
	// Limits.PostRetention.
	//
	// Posts used to be stored nowhere. lane_post appended the text to an event
	// and returned a serial, and that event was the only copy: an agent that
	// was not polling at that moment never saw it, a restart lost it, and
	// lane_read, the tool whose whole job is "read the lane", did not return
	// posts at all. It looked like it worked because the event reached
	// everybody, including agents who had no business receiving it.
	//
	// Keeping them here is what makes lane_post a message rather than a
	// notification: the event says a post happened, and a member who was
	// asleep, or who has just replayed after a crash, can still read what was
	// said. Announcements have always worked this way; posts now match.
	Posts []Post
	Owner string   // exclusive owner; "" when the channel is open
	Queue []string // agents waiting on an exclusive channel, in order
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

	// Declined remembers agents that left this lane DELIBERATELY.
	//
	// An agent that walks out and is put straight back has not been coordinated
	// with; it has been overruled. Reported from a live fleet by an agent that
	// left a lane it did not belong in and posted its reasons: "my very next
	// set_slot auto-joined me again, score UP from 0.1651 to 0.2289, same generic
	// evidence." Re-adding it overrode a decision instead of making one.
	//
	// This records lane_leave only. An eviction is somebody else's decision, and a
	// departure the sweep made on behalf of a crashed agent is not a decision at
	// all: neither should stop that agent being matched here later.
	//
	// It blocks AUTO-join, not membership: lane_join still works, because an agent
	// is allowed to change its mind, and the lane is still surfaced with its score
	// so it can. What it will not do is happen by itself, twice.
	Declined map[string]bool

	// Predicted is the lane's file footprint: what the work in here touches,
	// merged from every member's recorded prediction.
	//
	// RECORDED, never computed: same rule as Membership.Score and for the same
	// reason (SPEC-CHANNELS.md §4.3). A footprint recomputed at replay time
	// against a reindexed repository is a different footprint, which would match
	// different agents into different lanes and make the ledger a work of
	// fiction. Apply merges what the op carries and nothing else.
	Predicted []PredFile
}

// mergePredicted folds a new prediction into the lane's footprint, keeping the
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
	// Bounded: a lane that has accumulated a thousand files predicts nothing in
	// particular, and every future match against it would be noise.
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

// Membership records how an agent came to be in a channel.
//
// Every impure field here. Score, Threshold, ScorerID, ScorerVersion, Evidence
// is COPIED FROM THE OP, never computed. That is the whole replay contract
// (SPEC-CHANNELS.md §4.3): a similarity score recomputed next week against a
// reindexed repository is a different number, so a state machine that scores
// during replay reconstructs different membership and the ledger's hash chain
// stops meaning anything. Same discipline as liveness probe verdicts (SPEC §7).
//
// It doubles as the explainability record §10.3 requires: "why am I in this
// lane" is answered from here, without re-running a model that may no longer
// exist.
type Membership struct {
	Lane          string
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
	// member: the channel's own footprint must not grow until the agent is
	// actually in. It used to be dropped at that point instead of deferred, so
	// an agent promoted off the queue contributed nothing to what the channel
	// was understood to touch, and every later match against that channel scored
	// against a footprint missing a full member's files.
	Predicted []PredFile
}

// Announcement is channel traffic that must be acknowledged.
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
	Channel  string
	From     string
	Body     string
	Acked    map[string]bool
	Required map[string]bool // members at announce time, excluding the sender
	Retries  int
	// DepartedUnacked names members that left the lane still owing an
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

// Op kinds for channels. Wire names follow SPEC-CHANNELS.md, not the Go types.
const (
	OpLaneOpen      = "lane_open"
	OpLaneJoin      = "lane_join"
	OpLaneLeave     = "lane_leave"
	OpLaneSubscribe = "lane_subscribe"
	OpLaneExclusive = "lane_exclusive"
	OpLanePost      = "lane_post"
	OpLaneAnnounce  = "lane_announce"
	OpLaneAck       = "lane_ack"

	// Director powers: the coordinator role applied to channels (§8.1).
	OpLaneForceRelease = "lane_force_release"
	OpLaneEvict        = "lane_evict"
	OpLaneMerge        = "lane_merge"
	OpLaneClose        = "lane_close"
	OpLaneAdmit        = "lane_admit"
)

// ── open ─────────────────────────────────────────────────────────────────

func (s *State) applyLaneOpen(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	if l.AckedSerial == 0 {
		return nil, nil, ErrMustAck
	}
	id := cleanID(op.Channel)
	if id == "" {
		return nil, nil, errf("E_BAD_ARG", "give the lane a name", "lane id required")
	}
	if len(op.Text) > s.Limits.MaxBodyBytes {
		return nil, nil, errTooLarge("topic", s.Limits.MaxBodyBytes)
	}
	if _, exists := s.Channels[id]; exists {
		return nil, nil, errf("E_LANE_EXISTS", "join it instead of opening it", "lane %s already exists", id)
	}
	if len(s.Channels) >= s.Limits.MaxLanes {
		return nil, nil, errf("E_LANE_LIMIT",
			"a lane Lanes opened from a declaration is reclaimed once its last member "+
				"leaves, so lane_leave the ones you are done with. A lane somebody "+
				"opened by name outlives its members on purpose: standing lanes are "+
				"the point, so leaving one frees nothing: lane_merge it into the lane "+
				"that is the same work",
			"%d lanes (max)", len(s.Channels))
	}
	ch := &Channel{
		ID: id, Topic: op.Text, Key: coordKey(s.NodeID, s.Serial), Auto: op.Auto,
		Members: map[string]*Membership{}, Subs: map[string]bool{},
		OpenedBy: l.ID, Predicted: mergePredicted(nil, op.Predicted),
	}
	s.Channels[id] = ch
	evs := []Event{{Type: "lane.opened", Lane: l.ID, Data: map[string]any{
		"lane_id": id, "topic": op.Text,
	}}}
	// The opener is a member. A lane whose creator is not in it is a lane
	// nobody is working in, which is not a thing worth having.
	ch.Members[l.ID] = &Membership{
		Lane: l.ID, ScorerID: "explicit", JoinedSerial: s.Serial + 1,
	}
	if op.Exclusive {
		ch.Owner = l.ID
		evs = append(evs, Event{Type: "lane.exclusive", Lane: l.ID, Data: map[string]any{
			"lane_id": id, "owner": l.ID,
		}})
	}
	s.finish(&evs, now)
	ch.OpenedAt = evs[0].TS
	// The key goes back to the agent that just earned it. A key nobody is ever
	// handed is a mechanism nobody can use, which is what "the join path is
	// decorative" meant.
	return Result{
		"lane_id": id, "exclusive": op.Exclusive, "key": ch.Key,
		"key_hint": "declare this in `refs` on later set_slot calls and Lanes will " +
			"match you to this lane exactly, instead of guessing from your wording",
	}, evs, nil
}

// ── join ─────────────────────────────────────────────────────────────────

// memberFromOp builds the membership record a join op describes.
//
// One place, because the same record has to be produced from three:
// an immediate join, a promotion off the queue, and a merge. The two latter
// used to fabricate their own with a placeholder ScorerID and no score, which
// silently voided §10.3's explainability guarantee for every agent that did not
// join a lane the instant it asked.
func memberFromOp(agent string, op *Op, serial uint64) *Membership {
	m := &Membership{
		Lane: agent, Score: op.Score, Threshold: op.Threshold,
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
func (ch *Channel) promote(agent string, serial uint64) {
	m := ch.Pending[agent]
	if m == nil {
		// Queued before Pending existed, or by a path that recorded nothing.
		// Say "queue" honestly rather than claim a score we never had.
		m = &Membership{Lane: agent, ScorerID: "queue"}
	}
	m.JoinedSerial = serial
	delete(ch.Pending, agent)
	ch.Members[agent] = m
	// The footprint the agent declared while waiting now counts, because the
	// agent now counts.
	ch.Predicted = mergePredicted(ch.Predicted, m.Predicted)
}

func (s *State) applyLaneJoin(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	if l.AckedSerial == 0 {
		return nil, nil, ErrMustAck
	}
	ch := s.Channels[cleanID(op.Channel)]
	if ch == nil {
		return nil, nil, errf("E_NO_LANE", "lane_open it, or list lanes first", "no lane %s", op.Channel)
	}
	if _, already := ch.Members[l.ID]; already {
		return Result{"lane_id": ch.ID, "joined": true, "already": true, "key": ch.Key}, nil, nil
	}
	// A subagent already speaks here through its parent, and SPEC-CHANNELS.md
	// §8.2 is explicit that it must not join, queue or count separately.
	//
	// Letting it join was not merely redundant, it deadlocked: a subagent asking
	// to join the lane its own PARENT holds exclusively was queued behind that
	// parent: position 2, with a hint telling it to send the owner a request,
	// and the parent does not release until the subagent's work is done. Each
	// waits for the other. Meanwhile lane_post from that subagent already worked,
	// because speaking is what the inherited membership is for.
	if under := s.speaksFor(ch, l.ID); under != "" {
		return Result{
			"lane_id": ch.ID, "joined": true, "already": true, "under": under,
			"detail": "you are in this lane through " + under +
				", the agent that spawned you: subagents inherit lanes rather than " +
				"joining them, so you can post and announce here already and do not " +
				"count as a separate member",
		}, nil, nil
	}

	// An exclusive lane held by somebody else does not admit; it queues. That
	// is the difference between a refusal and coordination: a blocked agent
	// with a queue position has somewhere to be, one with a refusal has only
	// the option of ignoring it (SPEC-CHANNELS.md §5).
	if ch.Owner != "" && ch.Owner != l.ID {
		for i, q := range ch.Queue {
			if q == l.ID {
				return Result{
					"lane_id": ch.ID, "joined": false, "queued": true,
					"queue_position": i + 1, "owner": ch.Owner,
				}, nil, nil
			}
		}
		ch.Queue = append(ch.Queue, l.ID)
		if ch.Pending == nil {
			ch.Pending = map[string]*Membership{}
		}
		ch.Pending[l.ID] = memberFromOp(l.ID, op, 0) // serial filled in on promotion
		evs := []Event{{Type: "lane.queued", Lane: l.ID, To: ch.Owner, Data: map[string]any{
			"lane_id": ch.ID, "queue_position": len(ch.Queue), "owner": ch.Owner,
			"score": op.Score,
		}}}
		s.finish(&evs, now)
		return Result{
			"lane_id": ch.ID, "joined": false, "queued": true,
			"queue_position": len(ch.Queue), "owner": ch.Owner,
			"hint": "the lane is exclusive; send its owner a request, or wait to be admitted",
		}, evs, nil
	}

	m := memberFromOp(l.ID, op, s.Serial+1)
	ch.Members[l.ID] = m
	ch.Predicted = mergePredicted(ch.Predicted, op.Predicted)
	delete(ch.Subs, l.ID) // membership supersedes subscription
	evs := []Event{{Type: "lane.joined", Lane: l.ID, Data: map[string]any{
		"lane_id": ch.ID, "auto": op.Auto, "score": op.Score,
		"threshold": op.Threshold, "scorer": m.ScorerID, "evidence": op.Evidence,
		"members": len(ch.Members),
	}}}
	s.finish(&evs, now)
	return Result{
		"lane_id": ch.ID, "joined": true, "members": len(ch.Members),
		"topic": ch.Topic, "auto": op.Auto, "score": op.Score, "key": ch.Key,
		"key_hint": "declare this in `refs` on later set_slot calls and Lanes will " +
			"match you to this lane exactly, instead of guessing from your wording",
	}, evs, nil
}

// ── leave ────────────────────────────────────────────────────────────────

func (s *State) applyLaneExclusive(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	ch := s.Channels[cleanID(op.Channel)]
	if ch == nil {
		return nil, nil, errf("E_NO_LANE", "open or join it first", "no lane %s", op.Channel)
	}
	if _, ok := ch.Members[l.ID]; !ok {
		return nil, nil, errf("E_NOT_MEMBER", "lane_join first", "not a member of %s", ch.ID)
	}
	release := op.Mode == "release" || op.Mode == "shared"
	if release {
		if ch.Owner != l.ID {
			return nil, nil, errf("E_NOT_OWNER", "only the owner may release", "%s is owned by %q", ch.ID, ch.Owner)
		}
		evs := s.releaseExclusive(ch, "released by its owner")
		s.finish(&evs, now)
		return Result{"lane_id": ch.ID, "exclusive": ch.Owner != ""}, evs, nil
	}
	if ch.Owner != "" && ch.Owner != l.ID {
		return nil, nil, errf("E_LANE_EXCLUSIVE", "request access from the owner, or queue",
			"%s is already exclusive to %s", ch.ID, ch.Owner)
	}
	// Only the FIRST member may take a lane exclusively (§5). Letting the
	// fourth arrival lock out the three already working is not coordination.
	if len(ch.Members) > 1 && ch.Owner == "" {
		return nil, nil, errf("E_LANE_SHARED", "ask the other members to leave, or coordinate in the lane",
			"%s already has %d members; exclusivity is for the first", ch.ID, len(ch.Members))
	}
	ch.Owner = l.ID
	evs := []Event{{Type: "lane.exclusive", Lane: l.ID, Data: map[string]any{
		"lane_id": ch.ID, "owner": l.ID,
	}}}
	s.finish(&evs, now)
	return Result{"lane_id": ch.ID, "exclusive": true, "owner": l.ID}, evs, nil
}

func (s *State) releaseExclusive(ch *Channel, why string) []Event {
	// Name WHO stopped owning and WHY, and carry SPEC §9's caution.
	//
	// This emitted a bare event with only the lane id: no former owner, no
	// cause, no caution: while the two neighbouring release paths carry all
	// three. It is also the path a liveness sweep uses, which is the one case
	// where the caution matters most: a consumer that cannot tell a deliberate
	// release from a lapsed lease can read "released" as safe-to-take.
	former := ch.Owner
	ch.Owner = ""
	evs := []Event{{Type: "lane.released", Lane: former, Data: map[string]any{
		"lane_id": ch.ID, "former_owner": former, "cause": why,
		"caution": "the owner's coordination signal ended; this is not proof its work stopped",
	}}}
	// Everyone waiting is admitted at once: the thing they were waiting for is
	// gone, and admitting them one at a time would just be a slower queue.
	for _, q := range ch.Queue {
		if s.Lanes[q].Gone() {
			continue
		}
		ch.promote(q, s.Serial+1)
		evs = append(evs, Event{Type: "lane.joined", Lane: q, Data: map[string]any{
			"lane_id": ch.ID, "from_queue": true,
		}})
	}
	ch.Queue = nil
	return evs
}

// ── traffic ──────────────────────────────────────────────────────────────

func (s *State) applyLanePost(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
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
	// This carried the whole post, and channel events have no `To`, so
	// filterEvents delivered it to every authenticated lane on the board: a
	// non-member reading events_since received the text verbatim. The
	// member/subscriber/outsider distinction that lane_subscribe exists to draw
	// was collapsed: everyone got the same bodies. Direct mail was always
	// correct here, which is why the existing test passed; it only ever sent
	// mail, never posted.
	//
	// The serial is what a reader needs. Members and subscribers fetch the text
	// with lane_read, which checks who is asking.
	evs := []Event{{Type: "lane.post", Lane: speaker, Data: map[string]any{
		"lane_id": ch.ID, "from": l.ID, "on_behalf_of": speaker,
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
	return Result{"lane_id": ch.ID, "serial": serial}, evs, nil
}

func (s *State) applyLaneAnnounce(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	// The awareness gate (SPEC §6) applies here above all.
	//
	// An announcement is the strongest thing an agent can do to a lane: it
	// obliges every member to acknowledge it and re-pings them until they do.
	// Doing that without having read the board is exactly what the gate exists
	// to prevent: an agent that has just reattached after losing its context
	// could announce "FREEZE auth/token.go" while contradicting an announcement
	// made thirty seconds earlier by somebody else, and oblige the whole lane to
	// answer it.
	//
	// It was gated on lane_join and lane_open, so the WEAKER acts were checked
	// and the strongest was not. `lane_post` is deliberately still ungated:
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
	// Metadata only, for the same reason as lane.post above: an announcement is
	// the loudest thing on the board and its text still belongs to the lane.
	evs := []Event{{Type: "lane.announce", Lane: speaker, Data: map[string]any{
		"lane_id": ch.ID, "from": l.ID, "on_behalf_of": speaker,
		"bytes":    len(op.Body),
		"must_ack": len(req),
	}}}
	serial := s.finish(&evs, now)
	s.Announcements[serial] = &Announcement{
		Serial: serial, Channel: ch.ID, From: speaker, Body: op.Body,
		Acked: map[string]bool{}, Required: req, MadeAt: evs[0].TS,
		State: stateFor(req),
	}
	return Result{"lane_id": ch.ID, "serial": serial, "must_ack": len(req)}, evs, nil
}

// An announcement nobody has to acknowledge is already settled; leaving it
// "open" would park it in the board's unresolved column forever.
func stateFor(req map[string]bool) string {
	if len(req) == 0 {
		return AnnounceAcked
	}
	return AnnounceOpen
}

func (s *State) applyLaneAck(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
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
	evs := []Event{{Type: "lane.acked", Lane: l.ID, To: a.From, Data: map[string]any{
		"lane_id": a.Channel, "serial": a.Serial, "outstanding": outstanding,
	}}}
	if outstanding == 0 {
		a.State = AnnounceAcked
		evs = append(evs, Event{Type: "lane.announce_settled", Lane: a.From, Data: map[string]any{
			"lane_id": a.Channel, "serial": a.Serial,
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
// and ack_board, which is the documented checkpoint after context loss and so
// is exactly where an agent that has forgotten everything comes looking.
// UnackedFor returns the announcements this agent still owes an acknowledgement
// on: an EMPTY slice when there are none, never nil.
//
// The distinction is not pedantry here. ack_board is documented as the recovery
// checkpoint, and an omitted key left an agent that had just lost its context
// unable to tell "you missed nothing" from "this is not working" or "I am asking
// on the wrong lane". Reported as a defect by the first agent to reach for it
// that way, and it was right: a checkpoint has to answer, including with nothing.
func (s *State) UnackedFor(agent string) []Result {
	un := s.Unacked(agent)
	if len(un) == 0 {
		return []Result{}
	}
	out := make([]Result, 0, len(un))
	for _, a := range un {
		out = append(out, Result{
			"serial": a.Serial, "lane": a.Channel, "from": a.From, "body": a.Body,
			"made_at": a.MadeAt, "retries": a.Retries,
			"action": "you must call lane_ack with msg_serial " +
				strconv.FormatUint(a.Serial, 10) + " once you have read and accounted for this",
		})
	}
	return out
}

// LaneHistory returns what has been ANNOUNCED in a lane, for a member.
//
// This did not exist, and its absence was found the way these things are found:
// a reviewing agent joined a lane, could see neither the announcement that had
// been made before it arrived nor any way to ask for it, and had to message a
// human to be told what the lane was about.
//
// Announcements bind their ack requirement to the members present when they are
// made, which is right: arriving late must not saddle you with an obligation
// for something said before you existed. But "you do not OWE this" was silently
// implemented as "you cannot SEE this", and those are different. A lane is
// supposed to be shared context; a newcomer got none of it.
//
// Sharpest form of the bug: the notice sent to an admitted agent reads "you may
// start; read the lane first": an instruction naming no tool that could do it.
//
// Members only. An announcement's body is for the lane, not for anyone who can
// name it, which is the same rule the wake path follows.
func (s *State) LaneHistory(ch *Channel, agent string, limit int) []Result {
	if limit <= 0 {
		limit = 50
	}
	var all []*Announcement
	for _, a := range s.Announcements {
		if a.Channel == ch.ID {
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
			r["your_ack"] = "OWED: call lane_ack with msg_serial " +
				strconv.FormatUint(a.Serial, 10)
		}
		out = append(out, r)
	}
	return out
}

// ── helpers ──────────────────────────────────────────────────────────────

// MemberChannel resolves a lane the caller is a member of, by name. The read
// path's counterpart to memberChannel, which takes an Op.
func (s *State) MemberChannel(l *Lane, name string) (*Channel, error) {
	return s.memberChannel(l, &Op{Channel: name})
}

func (s *State) memberChannel(l *Lane, op *Op) (*Channel, error) {
	ch := s.Channels[cleanID(op.Channel)]
	if ch == nil {
		return nil, errf("E_NO_LANE", "lane_open or lane_join first", "no lane %s", op.Channel)
	}
	if s.speaksFor(ch, l.ID) == "" {
		return nil, errf("E_NOT_MEMBER", "lane_join first: subscribers read, members speak",
			"not a member of %s", ch.ID)
	}
	return ch, nil
}

// SpeaksFor exports speaksFor for callers that must distinguish a member (or a
// subagent acting under an ancestor's membership) from a mere subscriber.
func (s *State) SpeaksFor(ch *Channel, id string) string { return s.speaksFor(ch, id) }

// speaksFor returns the membership an agent is acting under in a channel:
// itself, or the ancestor whose membership it inherited. "" means neither.
//
// This is what makes spawning a subagent free (SPEC-CHANNELS.md §8.2). A
// subagent that had to join would be counted as a second occupant of its
// parent's work, which is not a collision (it is one agent's own helper) and
// on an exclusive lane it would queue behind its own parent forever.
//
// Bounded walk: a parent chain is a tree in practice, but a corrupted or
// hand-edited ledger could contain a cycle, and an unbounded walk inside the
// single writer loop would hang the whole daemon rather than fail one call.
func (s *State) speaksFor(ch *Channel, agent string) string {
	seen := map[string]bool{}
	for id := agent; id != "" && len(seen) < 16; {
		if _, ok := ch.Members[id]; ok {
			return id
		}
		if seen[id] {
			return "" // cycle
		}
		seen[id] = true
		l := s.Lanes[id]
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
		l := s.Lanes[id]
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

// cleanID normalises a channel id so "Auth Refactor" and "auth-refactor" are
// not two different lanes.
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

// yieldChannelOwnership releases every lane this agent holds exclusively,
// without touching its membership.
//
// Called when an agent leaves `active`: the same moment its directory claims
// end, and for the same reason. Ownership is the part that BLOCKS other agents,
// so a fleet must never stay wedged behind an agent that crashed. Membership is
// merely informative, costs nobody anything, and a dormant persistent agent
// that wakes should still find itself in the lanes it was working in.
//
// The honesty rule from SPEC §9 carries over unchanged and is stated in the
// event: the coordination signal ended, which is not proof the owner's work
// stopped or that the lane is safe to take.
func (s *State) yieldChannelOwnership(agent string) []Event {
	var evs []Event
	for _, id := range s.channelIDs() {
		ch := s.Channels[id]
		if ch.Owner != agent {
			continue
		}
		evs = append(evs, s.releaseExclusive(ch, "the owner stopped coordinating")...)
	}
	return evs
}

// reclaimFinishedLanes deletes lanes nobody is in and nobody owes anything to.
//
// Lanes had no way to end. `lane_merge` was the only path that removed one, and
// E_LANE_LIMIT told the operator to "close a finished lane first": naming a
// corrective action that did not exist, which is this codebase's most persistent
// failure mode in yet another place.
//
// It became urgent the moment declarations started opening lanes automatically:
// before that, 64 was a generous ceiling on lanes a human had chosen to create;
// after it, a fleet working through 64 unrelated tasks exhausts the cap for
// good, and every later declaration silently gets no lane.
//
// "Finished" is deliberately narrow. AUTO-OPENED, no members, nobody queued,
// and nothing outstanding. An agent that crashed still holds its membership
// until the sweep archives it, so a lane whose members are merely quiet is not
// touched.
//
// Auto-opened is the load-bearing one, and it follows from the paragraph above:
// the pressure on the cap comes from lanes Lanes itself opens per declaration,
// never from the ones a human chose to create. Reclaiming both destroyed the
// standing lanes that outliving your members is FOR: an agent returning to
// "release" found nothing, and the test protecting that property passed only
// because it never ran a sweep.
func (s *State) reclaimFinishedLanes() []Event {
	var evs []Event
	for _, id := range s.channelIDs() {
		ch := s.Channels[id]
		if !ch.Auto || len(ch.Members) > 0 || len(ch.Queue) > 0 {
			continue
		}
		// An announcement that was never acknowledged is the record that
		// something went unanswered, and the board renders announcements THROUGH
		// their lane, so reclaiming the lane does not delete the record, it
		// hides it. The datum survives in s.Announcements and nothing can see it
		// again, which is the worse half of losing it.
		//
		// Both non-acked states count. `unacked` in particular is the one that
		// matters: it means redelivery gave up, which is exactly when somebody
		// needs to still be able to find it. Testing only for `open` reclaimed
		// those lanes and took the evidence off the board.
		outstanding := false
		for _, a := range s.Announcements {
			if a.Channel == id && a.State != AnnounceAcked {
				outstanding = true
				break
			}
		}
		if outstanding {
			continue
		}
		// The lane's announcements go with it.
		//
		// Reclaiming the channel alone left them keyed by a lane id that no
		// longer existed, and LaneHistory selects purely by that id, so the
		// moment anyone opened a lane with the same id (and ids are derived from
		// the declaration, so identical work reuses one), lane_read handed a
		// stranger the previous lane's announcement bodies. Members-only content
		// to a non-member, surviving a restart.
		//
		// Two of my own changes combined to make this: automatic reclamation
		// created dead ids, and lane_read gave them a reader. Neither was wrong
		// alone.
		//
		// Safe to delete because reclamation only happens when every
		// announcement here is ACKED: settled, everyone accounted for it. The
		// ledger keeps the full record either way; this is live state, not the
		// audit trail.
		for serial, a := range s.Announcements {
			if a.Channel == id {
				delete(s.Announcements, serial)
			}
		}
		delete(s.Channels, id)
		evs = append(evs, Event{Type: "lane.reclaimed", Data: map[string]any{
			"lane_id": id, "topic": ch.Topic,
			"why": "the last member left and nothing is outstanding",
		}})
	}
	return evs
}

// departAllChannels removes an agent from every lane, for close and archive.
func (s *State) departAllChannels(agent string) []Event {
	var evs []Event
	for _, id := range s.channelIDs() {
		ch := s.Channels[id]
		if _, member := ch.Members[agent]; member {
			evs = append(evs, s.departChannel(ch, agent)...)
			continue
		}
		dequeue(ch, agent)
	}
	return append(evs, s.dropAckRequirements(agent)...)
}

// dequeue removes an agent from a channel's waiting list.
func dequeue(ch *Channel, agent string) {
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
// dropAckRequirementsIn releases what an agent owed a SINGLE lane, for when it
// leaves that lane rather than the board.
func (s *State) dropAckRequirementsIn(channel, agent string) []Event {
	return s.releaseAcks(agent, func(an *Announcement) bool { return an.Channel == channel })
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
		evs = append(evs, Event{Type: "lane.announce_settled", Lane: an.From, Data: map[string]any{
			"lane_id": an.Channel, "serial": an.Serial, "state": an.State,
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
	out := make([]string, 0, len(s.Channels))
	for id := range s.Channels {
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

// LaneMatch is one candidate lane for a piece of declared work.
type LaneMatch struct {
	Lane      string     `json:"lane"`
	Topic     string     `json:"topic"`
	Score     float64    `json:"score"`
	Shared    []PredFile `json:"shared,omitempty"`
	Members   int        `json:"members"`
	Owner     string     `json:"owner,omitempty"`
	AlreadyIn bool       `json:"already_member,omitempty"`
	// Declined: this agent left this lane on purpose. Still surfaced, never
	// auto-joined. See Channel.Declined.
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
	// is exactly what Lanes should have caught, and would have, on refs."
	SharedRefs []string `json:"shared_refs,omitempty"`

	// SharedIDs are the shared refs that NAME something, and they are the only
	// ones an automatic join may rest on. See identifyingRef.
	SharedIDs []string `json:"shared_ids,omitempty"`

	// Evidence is what this match actually rests on, slot-to-slot against the
	// member whose live declaration is closest, not against the lane's
	// accumulated union, which only grows. Relation is what Evidence.Classify
	// made of it, and it is what the decision reads.
	Evidence Evidence `json:"evidence,omitzero"`
	Relation Relation `json:"relation,omitempty"`
}

// evidenceAgainstMembers compares one live declaration against each member's own
// live declaration and keeps the strongest relation.
//
// Slot-to-slot, deliberately. Comparing against ch.Predicted: the union of
// everything every member has ever been predicted to touch: made a lane an
// easier target the longer it lived, because that union grows on every join and
// never shrinks. Measured: the same unrelated newcomer scored 0.0000 against a
// one-member lane and 0.1000 against the same lane with five.
//
// The union is still what generated this candidate, which is the job breadth is
// good for. It is not what decides.
// The third return says whether there was anything to judge AGAINST: at least
// one member holding a live declaration with a footprint. It is not a detail.
// "I compared you against this lane's members and none resembles you" and "this
// lane's members have declared nothing I could compare you to" are opposite
// facts, and scoring both as zero made every lane whose members had not yet
// called set_slot permanently invisible: including the ordinary case of an
// agent opening a lane for work it is about to start.
func (s *State) evidenceAgainstMembers(
	ch *Channel, mine Slot, myCWD, repo string, discount map[string]float64, lens RepoLens,
) (Evidence, Relation, bool) {
	best, bestRel := Evidence{SameRepo: true}, RelationNone
	compared := false
	for agent := range ch.Members {
		l := s.Lanes[agent]
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
// join and never shrinks, so a lane became an easier target the longer it lived,
// and a lane that matched more gained members and gained surface by gaining them.
// Measured before this changed: the same unrelated newcomer scored 0.0000 against
// a one-member lane and 0.1000 against the same lane with five, crossing a real
// fleet's join bar with no change to its work and none to the lane's topic.
//
// Breadth is the right property for FINDING candidates and the wrong one for
// judging them. The score now comes from the closest single live declaration,
// which is the thing an agent can actually be duplicating.
// There is deliberately no fallback to unionScore. Keeping one meant that a lane
// where NO member matched still scored on the merged footprint, which is the
// accretion bug itself, surviving in the one branch that looked harmless. If no
// live declaration in the lane resembles this one, the honest score is zero and
// `worthless` drops the candidate.
func judgedScore(union float64, ev Evidence, compared bool) float64 {
	if !compared {
		// Nothing was measured, so there is no verdict to prefer over the union.
		// Scoring this zero does not express doubt: it deletes the lane from
		// every future match, silently and permanently, and a lane opened for work
		// that has not been declared yet is the commonest shape there is: the
		// channel e2e opens one on exactly that path and it vanished.
		//
		// The union may still overstate a lane that many agents joined. That is a
		// worse estimate; invisibility is not an estimate at all.
		return union
	}
	return ev.Semantic
}

// occupied reports whether anybody is actually in a lane.
//
// A lane nobody is in cannot be evidence that somebody else is doing your work,
// because there is no somebody. Matching exists to stop two AGENTS duplicating
// each other, so "another lane is already pursuing the same objective" is simply
// false about an empty lane, and "join it to coordinate" sends an agent to talk
// to an empty room. Found on a live board, not in a suite: two lanes whose
// members had all been swept were still being offered, with `members=0` printed
// in the suggestion itself.
//
// Empty lanes are not a transient state to wait out. A lane a human opened on
// purpose outlives its members deliberately, and only auto-opened ones are ever
// reclaimed, so they stay findable by name and joinable on purpose. What they
// stop doing is claiming to be occupied.
//
// A queue counts: an agent waiting on an exclusive lane has not got in yet, but
// it is certainly working on that lane's subject.
func occupied(ch *Channel) bool {
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
// that: two lanes that had deliberately partitioned the repository between them
// both declared goal:green-main, because both wanted main green. Under a rule
// that treats every ref as an identifier, they auto-join.
//
//	pr:1231, issue:88, incident:farm-down, cve:…, commit:…  name a thing
//	key:9f3c…                                               names a DECISION
//	goal:…, gate:…, area:…, epic:…                          name a wish
//
// `key` is Lanes' own coordination key and the only entry here Lanes can verify
// see coordkey.go. It arrives already validated: every path that reaches this
// function passes refs through State.validatedRefs first, which strikes out a
// key the declaring agent does not hold. `lane` used to sit in this list and no
// longer does. A lane id is a string an agent can write for a lane it merely
// believes it belongs in, and treating it as identity turned a belief into a
// verified fact: the exact laundering the opaque key exists to prevent.
//
// Unknown namespaces are treated as labels, which is the safe direction: an
// unrecognised ref surfaces the lane and lets the agent decide, rather than
// committing it on a word Lanes does not understand.
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

// MatchLanesWith is MatchLanes with a footprint overlay for lanes whose own is
// empty: those opened before a scorer finished indexing. Still pure: the
// overlay is computed at the edge and handed in, exactly like the score.
func (s *State) MatchLanesWith(agent string, pred []PredFile, overlay map[string][]PredFile, limit int) []LaneMatch {
	return s.MatchLanesRefs(agent, pred, nil, overlay, limit)
}

// MatchLanesRefs is MatchLanesWith plus the objective ids the declaring agent
// gave, so a match can report DECLARED overlap alongside the inferred score. See
// LaneMatch.SharedRefs.
func (s *State) MatchLanesRefs(
	agent string, pred []PredFile, refs []string, overlay map[string][]PredFile, limit int,
) []LaneMatch {
	return s.MatchLanesEvidence(agent, Slot{Refs: refs, Predicted: pred}, "", "", nil, overlay, limit)
}

// MatchLanesEvidence is the full form: it carries the declaring SLOT and the
// agent's location, so every match can report typed evidence computed against
// each member's own live declaration rather than the lane's accumulated union.
func (s *State) MatchLanesEvidence(
	agent string, mine Slot, myCWD, repo string, lens RepoLens,
	overlay map[string][]PredFile, limit int,
) []LaneMatch {
	// A coordination key the declaring agent does not hold is struck out before
	// anything is compared, here rather than at ingress: the declaration is
	// stored exactly as the agent wrote it, and only what Lanes will ACT on is
	// filtered. Rejecting it in the fold instead would make an old ledger stop
	// replaying, and refusing the whole set_slot would let one bad ref lose a
	// declaration Lanes exists to hear.
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
	var out []LaneMatch
	for _, id := range s.channelIDs() {
		ch := s.Channels[id]
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
		out = append(out, LaneMatch{
			Lane: ch.ID, Topic: ch.Topic, Score: score, Shared: shared,
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
		return out[i].Lane < out[j].Lane // deterministic ties
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ubiquityDiscount measures how much evidence each file is actually worth here.
//
// A file that appears in almost every lane's footprint cannot distinguish
// between them. Justfile, .github/workflows/ci.yml, CMakeLists.txt, llms-full.txt
// every project has them, they co-change with everything, and so they turn up
// as "shared" between any two agents who happen to work in the same repository.
//
// Two agents reported this within an hour of each other and neither was wrong.
// One was auto-joined to a lane on evidence that was four-fifths repo-root files
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
// A file in one lane keeps its full weight. A file in every lane keeps almost
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
		fp := s.Channels[id].Predicted
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
		// Linear in the share of lanes that contain it: present in one lane of
		// many → ~1, present in all → ~0. Floored so evidence is discounted, never
		// erased: a file every lane touches is weak evidence, not counter-evidence.
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

// ── the director: coordinator powers over channels (SPEC-CHANNELS.md §8.1) ──
//
// The spec calls this role "director". It is NOT a new role: it is the existing
// `coordinator` (SPEC §5), scoped to channels. Inventing a second privileged
// role would mean a second grant path to get right, and the existing one already
// has the property that matters: a human grants it, and no agent can promote
// itself (applyGrantRole is admitted only on the daemon's admin path).
//
// Every power here is one a lane owner already has over its own lane. The
// director's addition is being able to use them on somebody else's, which is
// what unsticks a fleet whose owner crashed, and every one of them is
// ledgered and announced, never silent.

func (s *State) directorOf(l *Lane, op *Op) (*Channel, error) {
	if !l.IsCoordinator() {
		return nil, errf("E_NOT_COORDINATOR", "ask a human to grant the coordinator role",
			"only a coordinator may administer another agent's lane")
	}
	ch := s.Channels[cleanID(op.Channel)]
	if ch == nil {
		return nil, errf("E_NO_LANE", "check the lane id", "no lane %s", op.Channel)
	}
	return ch, nil
}

// applyLaneForceRelease strips exclusivity from a lane the caller does not own.
//
// For the case the queue cannot solve on its own: an owner whose agent is gone
// but whose lane is still locked. The holder is named in the event, so this is
// never silent: same rule as force_release on a directory claim (SPEC §9).
func (s *State) applyLaneForceRelease(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	ch, err := s.directorOf(l, op)
	if err != nil {
		return nil, nil, err
	}
	if ch.Owner == "" {
		return Result{"lane_id": ch.ID, "released": false, "reason": "not exclusive"}, nil, nil
	}
	former := ch.Owner
	evs := []Event{{Type: "lane.force_released", Lane: l.ID, To: former, Data: map[string]any{
		"lane_id": ch.ID, "former_owner": former, "by": l.ID, "note": op.Note,
		"caution": "a coordinator released this; the former owner may still be working: verify",
	}}}
	evs = append(evs, s.releaseExclusive(ch, "forced by a coordinator")...)
	s.finish(&evs, now)
	return Result{"lane_id": ch.ID, "released": true, "former_owner": former}, evs, nil
}

// applyLaneEvict removes an agent from a lane it should not be in.
//
// This is also how "move an agent between lanes" works: evict, and the agent
// joins the right one. A single move op would have to guess which lane is right,
// and getting that wrong silently relocates somebody's work.
func (s *State) applyLaneEvict(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
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
	// is not on the lane and moves on, and the moment the owner leaves, the agent
	// it just tried to remove is promoted to OWNER of that lane. Observed
	// exactly: evict(waiter) returned evicted:false, then evict(owner) left
	// owner="waiter".
	//
	// Removing somebody from a lane has to mean removing them, so eviction takes
	// them out of the queue as well.
	if _, isMember := ch.Members[target]; !isMember {
		if i := slices.Index(ch.Queue, target); i >= 0 {
			ch.Queue = slices.Delete(ch.Queue, i, i+1)
			evs := []Event{{Type: "lane.evicted", Lane: target, To: target, Data: map[string]any{
				"lane_id": ch.ID, "by": l.ID, "note": op.Note, "from_queue": true,
				"detail": "a coordinator removed you from this lane's queue; you are no " +
					"longer waiting for it and will not be admitted",
			}}}
			s.finish(&evs, now)
			return Result{"lane_id": ch.ID, "evicted": true, "agent": target, "from_queue": true}, evs, nil
		}
		return Result{
			"lane_id": ch.ID, "evicted": false, "reason": "not a member",
			"detail": "nobody by that name is in this lane or waiting for it; check " +
				"the agent id against the board",
		}, nil, nil
	}
	evs := []Event{{Type: "lane.evicted", Lane: target, To: target, Data: map[string]any{
		"lane_id": ch.ID, "by": l.ID, "note": op.Note,
		"detail": "a coordinator removed you from this lane; your work is untouched",
	}}}
	evs = append(evs, s.departChannel(ch, target)...)
	s.finish(&evs, now)
	return Result{"lane_id": ch.ID, "evicted": true, "agent": target}, evs, nil
}

// applyLaneMerge folds one lane into another when they drifted into the same
// work: the case SPEC-CHANNELS.md §11 leaves open, resolved by a human-granted
// coordinator rather than by a score, because merging is destructive to context
// and a threshold is the wrong thing to trust with it.
// carryQueue moves the source lane's waiters somewhere real.
//
// Dropping them is the one thing that must not happen, and it is what used to
// happen: src.Queue=[waiter] became dst.Queue=[] and the waiter belonged to
// neither lane: blocked forever behind an owner that no longer existed.
//
// Where they go depends on the destination, and both answers give the agent
// what it was waiting for. If dst is exclusive they are still blocked, so they
// keep waiting, but on a lane that exists, in a queue they can be promoted out
// of. If dst is open, the thing they were waiting for is gone, so waiting is
// over and they are admitted.
func (s *State) carryQueue(src, dst *Channel) (queued, admitted int) {
	for _, id := range src.Queue {
		if _, already := dst.Members[id]; already {
			continue
		}
		if s.Lanes[id].Gone() {
			continue // the waiter is gone for good; nothing to carry
		}
		if dst.Owner != "" {
			if carryWaiter(src, dst, id) {
				queued++
			}
			continue
		}
		// Carried across with whatever it was queued on the SOURCE lane with:
		// a merge changes which lane the work lives in, not why the agent
		// belongs in it.
		if m := src.Pending[id]; m != nil {
			m.JoinedSerial = s.Serial + 1
			dst.Members[id] = m
			// The same rule as promote, and it has to be repeated here because
			// this is a second door into membership. A queued agent's footprint
			// is deliberately excluded from its own lane's Predicted: it is not
			// a member yet, so merging src.Predicted into dst does not carry
			// it. Without this, an agent that walks in through a merge instead
			// of a promotion is a full member whose files the lane has no
			// record of, which is exactly the hole the promote fix closed.
			dst.Predicted = mergePredicted(dst.Predicted, m.Predicted)
		} else {
			dst.Members[id] = &Membership{Lane: id, ScorerID: "merge", JoinedSerial: s.Serial + 1}
		}
		admitted++
	}
	return queued, admitted
}

// carryWaiter moves one still-blocked agent to the destination's queue, with
// the provenance it was queued under: a merge changes which lane the work lives
// in, not why the agent belongs in it.
func carryWaiter(src, dst *Channel, id string) bool {
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

// carryAnnouncements repoints outstanding traffic at the surviving lane.
//
// Left naming the deleted lane they are countable on NO board: invisible on
// the source because it is gone, invisible on the destination because they name
// the wrong id: while still obliging their members to acknowledge them. That
// is the abandoned-announcement failure mode exactly.
func (s *State) carryAnnouncements(src, dst *Channel) {
	for _, ser := range s.announcementSerials() {
		if an := s.Announcements[ser]; an.Channel == src.ID {
			an.Channel = dst.ID
		}
	}
}

// mergeNotices tells everyone the merge moved.
//
// Their old lane is GONE, so an agent not told keeps addressing a lane that no
// longer exists and every call fails for a reason it cannot guess. Same
// category as an admission or an eviction. Three groups, and the ones still
// queued matter most, because nothing else in their world would ever change.
func mergeNotices(src, dst *Channel, by string, wasHere []string) []Event {
	var evs []Event
	// The DESTINATION's people are affected too, and were told nothing.
	//
	// Their lane silently gains another lane's members, its predicted
	// footprint, and its outstanding announcements, which they may now be
	// required to acknowledge. Only the moved side was woken, so the
	// destination's owner could carry on believing its lane was unchanged and
	// still exclusively its own while a whole other lane had been folded in.
	//
	for _, id := range wasHere {
		if id == by {
			continue // the coordinator did this; its own tool result said so
		}
		evs = append(evs, Event{Type: "lane.absorbed", Lane: id, Data: map[string]any{
			"lane_id": dst.ID, "merged_from": src.ID, "merged_by": by,
			"gained": len(src.Members),
		}})
	}
	base := func() map[string]any {
		return map[string]any{
			"lane_id": dst.ID, "merged_from": src.ID, "merged_by": by,
			"members": len(dst.Members),
		}
	}
	for _, id := range sortedKeys(src.Members) {
		if _, in := dst.Members[id]; in && id != by {
			evs = append(evs, Event{Type: "lane.joined", Lane: id, Data: base()})
		}
	}
	for _, id := range src.Queue {
		if _, in := dst.Members[id]; in {
			d := base()
			d["from_queue"] = true
			evs = append(evs, Event{Type: "lane.joined", Lane: id, Data: d})
			continue
		}
		if pos := slices.Index(dst.Queue, id); pos >= 0 {
			d := base()
			d["queue_position"], d["owner"] = pos+1, dst.Owner
			evs = append(evs, Event{Type: "lane.requeued", Lane: id, Data: d})
		}
	}
	return evs
}

// applyLaneClose retires a finished lane that nothing will ever reclaim.
//
// Auto-opened lanes end by themselves: reclaimFinishedLanes deletes them once
// the last member leaves. Lanes a human opened deliberately do NOT, and that is
// correct: outliving your members is what a standing lane is FOR. The gap was
// that nothing could end one either, ever, by any path. A board accumulated
// finished lanes permanently, and E_LANE_LIMIT advised "lane_leave the ones you
// are done with", which does nothing for exactly these: naming a corrective
// action that does not work is this codebase's most persistent failure mode, and
// this was another instance of it.
//
// A coordinator can now say so explicitly. That fits the director rule: every
// director power is one a lane owner already has over its own lane: with the
// honest caveat that for THIS one no owner had it either. It is the same
// decision reclaimFinishedLanes makes automatically, made on purpose by somebody
// accountable, and ledgered under their name.
//
// Deliberately refuses an OCCUPIED lane rather than emptying it. Closing a lane
// with members in it would evict them as a side effect of tidying up, and a
// coordinator that wants that has lane_evict, which says what it does. Same for
// an unacknowledged announcement: it is the record that something went
// unanswered, and the board renders announcements through their lane, so closing
// over one hides evidence rather than settling it.
func (s *State) applyLaneClose(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	ch := s.Channels[cleanID(op.Channel)]
	if ch == nil {
		return nil, nil, errf("E_NO_LANE", "check the lane id", "no lane %s", op.Channel)
	}
	// The agent that OPENED a lane may retire it, without the coordinator role.
	//
	// lane_open is unprivileged and advertised, so an agent could create a lane
	// and then never end it, and the refusal it got said "only a coordinator may
	// administer ANOTHER AGENT'S lane" about its own. Telling somebody they may
	// not touch their own thing, in words describing somebody else's, is worse
	// than the missing power.
	//
	// Narrower than directorOf on purpose, and placed here rather than in it:
	// closing your own finished lane is not the same act as merging your lane
	// into a stranger's, and directorOf gates both. Every other guard below still
	// applies: an opener cannot close a lane somebody is in, or one holding an
	// unanswered announcement, any more than a coordinator can.
	if ch.OpenedBy != l.ID {
		if _, err := s.directorOf(l, op); err != nil {
			return nil, nil, err
		}
	}
	if occupied(ch) {
		return nil, nil, errf("E_LANE_OCCUPIED",
			"lane_evict the members first, or leave it: a lane with agents in it is "+
				"somebody's working context, not clutter",
			"lane %s still has %d member(s) and %d queued",
			ch.ID, len(ch.Members), len(ch.Queue))
	}
	for _, a := range s.Announcements {
		if a.Channel == ch.ID && a.State != AnnounceAcked {
			return nil, nil, errf("E_ANNOUNCE_OUTSTANDING",
				"the board shows announcements through their lane, so closing this one "+
					"would hide an unanswered obligation rather than settle it",
				"lane %s has an announcement nobody has acknowledged", ch.ID)
		}
	}
	// Its announcements go with it, for the reason reclaimFinishedLanes gives:
	// leaving them keyed by a dead id hands the next lane with that id a
	// stranger's history. Safe because every one of them is settled.
	for serial, a := range s.Announcements {
		if a.Channel == ch.ID {
			delete(s.Announcements, serial)
		}
	}
	delete(s.Channels, ch.ID)
	evs := []Event{{Type: "lane.closed", Lane: l.ID, Data: map[string]any{
		"lane_id": ch.ID, "topic": ch.Topic, "by": l.ID, "note": op.Note,
		"why": "closed by a coordinator; it was empty and everything in it was settled",
	}}}
	s.finish(&evs, now)
	return Result{"lane_id": ch.ID, "closed": true, "topic": ch.Topic}, evs, nil
}

func (s *State) applyLaneMerge(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	src, err := s.directorOf(l, op)
	if err != nil {
		return nil, nil, err
	}
	dst := s.Channels[cleanID(op.To)]
	if dst == nil {
		return nil, nil, errf("E_NO_LANE", "check the destination lane id", "no lane %s", op.To)
	}
	if dst.ID == src.ID {
		return nil, nil, errf("E_BAD_ARG", "name two different lanes", "cannot merge %s into itself", src.ID)
	}
	// Who was already in the destination, BEFORE anything moves. Taken here
	// because mergeNotices runs after the merge and could not otherwise tell
	// the people who were already there from the ones who just arrived: the
	// arrivals get their own notice, and two notices for one event is how a
	// wake channel becomes noise.
	wasHere := sortedKeys(dst.Members)

	// Exclusivity does not survive a merge: the destination may already have
	// members, and silently locking them out of their own lane would be a
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

	delete(s.Channels, src.ID)
	evs := []Event{{Type: "lane.merged", Lane: l.ID, Data: map[string]any{
		"from": src.ID, "into": dst.ID, "moved": moved, "by": l.ID, "note": op.Note,
		"queued": queued, "admitted": admitted,
		"detail": "these two lanes were the same work; read " + dst.ID + " for the full picture",
	}}}
	evs = append(evs, mergeNotices(src, dst, l.ID, wasHere)...)
	s.finish(&evs, now)
	return Result{
		"from": src.ID, "into": dst.ID, "moved": moved,
		"queued": queued, "admitted": admitted, "members": len(dst.Members),
	}, evs, nil
}

// applyLaneAdmit adds another agent to a lane, on a coordinator's authority.
//
// This is the approval half of `director_required` (SPEC-CHANNELS.md §8.1): with
// the gate on, scoring stops auto-joining and an agent waits to be admitted, so
// there has to be an act that admits it. It is also useful with the gate off,
// pulling somebody into work they should be part of but did not match.
//
// The recorded score travels through unchanged when the director is acting on a
// match, so a gated join is exactly as explainable as an automatic one; §10.3
// does not weaken because a human-granted role was in the loop.
func (s *State) applyLaneAdmit(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	ch, err := s.directorOf(l, op)
	if err != nil {
		return nil, nil, err
	}
	target := op.To
	if target == "" {
		return nil, nil, errf("E_BAD_ARG", "name the agent to admit", "`to` is required")
	}
	t := s.Lanes[target]
	if t == nil || t.Status == StatusClosed || t.Status == StatusArchived {
		return nil, nil, errf("E_NO_LANE", "check the board for live agents", "no live agent %q", target)
	}
	if _, already := ch.Members[target]; already {
		return Result{"lane_id": ch.ID, "admitted": true, "already": true}, nil, nil
	}
	ch.Members[target] = &Membership{
		Lane: target, Score: op.Score, Threshold: op.Threshold,
		ScorerID: firstNonEmpty(op.ScorerID, "director"), ScorerVersion: op.ScorerVersion,
		Evidence: op.Evidence, JoinedSerial: s.Serial + 1,
	}
	ch.Predicted = mergePredicted(ch.Predicted, op.Predicted)
	delete(ch.Subs, target)
	dequeue(ch, target)
	evs := []Event{{Type: "lane.joined", Lane: target, To: target, Data: map[string]any{
		"lane_id": ch.ID, "admitted_by": l.ID, "members": len(ch.Members),
		"score": op.Score, "note": op.Note,
	}}}
	s.finish(&evs, now)
	return Result{
		"lane_id": ch.ID, "admitted": true, "agent": target,
		"members": len(ch.Members),
	}, evs, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
