package core

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strings"
	"time"
)

// Admit rejects an op arriving from a CALLER. Not called during replay.
//
// The distinction is the whole point, and it cost a daemon its own history to
// learn: this check first went into Apply, and Apply is also the fold that
// replays the ledger. A ledger holding ops that were legal when they were
// written then failed to replay under the stricter rule, and the daemon refused
// to start ("replay apply serial 12: E_EMPTY_BODY") on data it had itself
// written and acknowledged.
//
// So Apply must accept everything it has ever accepted, forever. Anything that
// tightens what callers may DO belongs here, at ingress, where it binds new ops
// and leaves history alone. (The size limits inside Apply carry the same latent
// hazard: lower MaxBodyBytes and an existing ledger stops replaying. They
// predate this and are left rather than moved blind, but new rules go here.)
func Admit(op *Op, lim Limits) error {
	// Bounds on replayed metadata, applied at ingress for the reason above: the
	// same strings are already in ledgers on disk, and rejecting them in Apply
	// would stop those daemons booting.
	//
	// Everything here ends up in State and is therefore re-read into memory on
	// every start, forever. The count of dirs and refs was bounded and the
	// SIZE of each was not, which bounds nothing: sixteen refs of two megabytes
	// each is thirty-two megabytes of permanent ledger, accepted silently. The
	// probe that found this pushed a 2 MiB session_id and a slot holding
	// 100,000 holds, and the board took both.
	//
	// A hold is a host resource name ("port:8080"), a ref a file path, an
	// AgentInfo field a harness name: the honest values are tens of bytes, so
	// these ceilings are three orders of magnitude above real use and only ever
	// catch a mistake or an abuse.
	if err := boundStrings(lim.MaxPathBytes, "dirs", op.Dirs); err != nil {
		return err
	}
	if err := boundStrings(lim.MaxPathBytes, "refs", op.Refs); err != nil {
		return err
	}
	if len(op.Holds) > lim.MaxDirs {
		return errTooLarge("holds", lim.MaxDirs)
	}
	if err := boundStrings(lim.MaxPathBytes, "holds", op.Holds); err != nil {
		return err
	}
	if len(op.SessionID) > lim.MaxNameBytes {
		return errTooLarge("session_id", lim.MaxNameBytes)
	}
	if a := op.Agent; a != nil {
		for field, v := range map[string]string{
			"agent.harness": a.Harness, "agent.version": a.Version,
			"agent.surface": a.Surface, "agent.model": a.Model,
			"agent.provider": a.Provider, "agent.effort": a.Effort,
			"agent.title": a.Title, "agent.project": a.Project,
			"agent.branch": a.Branch, "agent.host": a.Host,
		} {
			if len(v) > lim.MaxNameBytes {
				return errTooLarge(field, lim.MaxNameBytes)
			}
		}
		// A cwd is a PATH, and was bounded as if it were a name. 128 bytes is
		// generous for a model or a branch and ordinary for a working
		// directory: a macOS temp directory alone reaches ninety, and any
		// checkout a few levels inside a home directory passes it. The whole
		// register_lane was then refused, so the agent could not coordinate AT
		// ALL, over a descriptive field. Relaxing an admission bound is safe in
		// the direction that matters: Admit runs only on ingress, so nothing
		// already in a ledger becomes inadmissible.
		if len(a.CWD) > lim.MaxPathBytes {
			return errTooLarge("agent.cwd", lim.MaxPathBytes)
		}
	}
	switch op.Kind {
	case OpLaneAnnounce:
		// An announcement with nothing in it obliges every member to
		// acknowledge nothing, and re-pings them until they do. The UPPER bound
		// on a body was checked and the lower one was not.
		//
		// Not hypothetical: a whole coordination channel between two agents ran
		// on empty announcements, because the caller sent the text under the
		// wrong key and the missing value became "". Each returned a serial and
		// a must_ack count, so it looked delivered from the sending side, while
		// the receiving agent saw a lane full of obligations that said nothing
		// and had to ask a human what was going on.
		if strings.TrimSpace(op.Body) == "" {
			return errf("E_EMPTY_BODY",
				"pass the announcement text as `body`",
				"`body` is empty: an announcement needs something to say, because it "+
					"obliges every member to acknowledge it, and an empty one obliges "+
					"them to acknowledge nothing")
		}
	case OpLanePost:
		// A post obliges nobody, so an empty one is noise rather than a false
		// obligation, but it is still an event delivered to every member, and
		// the cause is the same slip.
		if strings.TrimSpace(op.Body) == "" {
			return errf("E_EMPTY_BODY", "pass the text as `body`",
				"`body` is empty: a post needs something to say")
		}
	}
	return nil
}

// boundStrings rejects the first oversized element, naming it by index so the
// caller can find it in a list of sixteen.
func boundStrings(max int, what string, vals []string) error {
	for i, v := range vals {
		if len(v) > max {
			return errTooLarge(what+"["+itoa(i)+"]", max)
		}
	}
	return nil
}

// Op is the single command type. The ledger stores ops verbatim (command
// sourcing); all impure inputs are recorded in the op so replay applies
// decisions rather than recomputing them (SPEC §2, §4).
type Op struct {
	Kind string `json:"kind"`

	// Actor resolution. Token authenticates (live path); Lane is set by Apply
	// and used on replay (the engine blanks it on ingress: unforgeable).
	Token string `json:"-"`
	Lane  string `json:"lane,omitempty"`

	// register_lane / resume_lane / update_lane
	Name        string     `json:"name,omitempty"`
	Description string     `json:"description,omitempty"`
	PID         int        `json:"pid,omitempty"`
	ProcStart   int64      `json:"proc_start,omitempty"`
	NewToken    string     `json:"token,omitempty"` // engine-generated; encrypted at rest
	Nonce       string     `json:"nonce,omitempty"` // encrypted at rest
	ResumeID    string     `json:"resume_id,omitempty"`
	SessionID   string     `json:"session_id,omitempty"` // harness session, for hook lookup
	Agent       *AgentInfo `json:"agent,omitempty"`      // who is behind the lane (descriptive only)
	Parent      string     `json:"parent,omitempty"`     // the agent that spawned this one (§8.2)
	// ParentNonce is the one-time secret the parent issued for this child.
	//
	// Parent alone is a claim anyone can make; this is the proof. A parent that
	// actually spawned a child can hand it a secret: same process, same trust
	// domain, and nobody else has it.
	ParentNonce string   `json:"parent_nonce,omitempty"`
	LaneKind    LaneKind `json:"lane_kind,omitempty"`

	// set_slot / clear_slot
	SlotID string   `json:"slot_id,omitempty"`
	Text   string   `json:"text,omitempty"`
	Dirs   []string `json:"dirs,omitempty"`
	Refs   []string `json:"refs,omitempty"` // objective ids: pr:1186, gate:typos …
	// Activity is the ROLE this agent has on the work (implement, review, test).
	// Holds are exclusive host resources it needs (port:8080, lock:.git/index).
	Activity string   `json:"activity,omitempty"`
	Holds    []string `json:"holds,omitempty"`

	// send_message / respond / ack_message
	To          string       `json:"to,omitempty"`
	MsgType     string       `json:"msg_type,omitempty"`
	Body        string       `json:"body,omitempty"` // encrypted at rest
	DeadlineSec int          `json:"deadline_sec,omitempty"`
	OpID        string       `json:"op_id,omitempty"`
	MsgSerial   uint64       `json:"msg_serial,omitempty"`
	Disposition string       `json:"disposition,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"` // send_message (A2)

	// put_blob (bytes already staged off-thread; op carries the recorded id)
	Blob string `json:"blob,omitempty"`
	Mime string `json:"mime,omitempty"`
	Size int64  `json:"size,omitempty"`

	// claim / release
	Path string `json:"path,omitempty"`
	Mode string `json:"mode,omitempty"`
	Note string `json:"note,omitempty"`

	// sweep: recorded impure inputs (SPEC §7)
	//
	// GiveUpAnnounce lists announcements whose redelivery budget is spent. The
	// count lives in the engine (it is delivery bookkeeping, not coordination
	// state), so like every other impure sweep input it arrives RECORDED,
	// replay marks exactly the same announcements without counting anything.
	GiveUpAnnounce []uint64 `json:"give_up_announce,omitempty"`
	DeadLanes      []string `json:"dead_lanes,omitempty"`
	StaleLanes     []string `json:"stale_lanes,omitempty"`
	AlivePIDs      []int    `json:"alive_pids,omitempty"`

	// mark_delivered: ledgered pending→delivered receipts
	MsgSerials []uint64 `json:"msg_serials,omitempty"`

	// Channels (SPEC-CHANNELS.md). "Channel" is the Go name; the wire name is
	// "lane", which is the vocabulary the protocol and the spec both use.
	Channel   string `json:"channel,omitempty"`
	Exclusive bool   `json:"exclusive,omitempty"`

	// Recorded scoring inputs: the replay contract (SPEC-CHANNELS.md §4.3).
	//
	// These are IMPURE and therefore travel in the op, exactly as the sweep's
	// PID verdicts do. Apply must treat them as fact: it may not invoke a
	// scorer, read the filesystem, or recompute any of them. Recomputing a
	// similarity score during replay yields a different number against a
	// reindexed repository, which would reconstruct different membership and
	// make the hash chain meaningless.
	Score         float64  `json:"score,omitempty"`
	Threshold     float64  `json:"threshold,omitempty"`
	ScorerID      string   `json:"scorer_id,omitempty"`
	ScorerVersion string   `json:"scorer_version,omitempty"`
	Evidence      []string `json:"evidence,omitempty"`
	Auto          bool     `json:"auto,omitempty"`

	// Predicted is the recorded file footprint of the declaring work: what a
	// scorer said this agent will touch. Recorded for the same reason the score
	// is: it decides lane membership, and recomputing it on replay reconstructs
	// a different fleet.
	Predicted []PredFile `json:"predicted,omitempty"`
}

// Op kinds.
const (
	OpRegisterLane       = "register_lane"
	OpResumeLane         = "resume_lane"
	OpWakeLane           = "wake_lane"
	OpActivityCheckpoint = "activity_checkpoint"
	OpAckBoard           = "ack_board"
	OpUpdateLane         = "update_lane"
	OpBindSession        = "bind_session"
	OpCloseLane          = "close_lane"
	OpHeartbeat          = "heartbeat"
	OpSetSlot            = "set_slot"
	OpClearSlot          = "clear_slot"
	OpSendMessage        = "send_message"
	OpRespond            = "respond"
	OpAckMessage         = "ack_message"
	OpClaim              = "claim"
	OpRelease            = "release"
	OpSweep              = "sweep"
	OpMarkDelivered      = "mark_delivered"
	OpPutBlob            = "put_blob"
	OpGrantRole          = "grant_role"
	OpPruneLane          = "prune_lane"
	// OpVouchChild is how a parent proves it really is spawning a subagent.
	OpVouchChild   = "vouch_child"
	OpForceRelease = "force_release"
)

// Result is the caller-facing op result.
type Result map[string]any

// Apply executes op at time now. The ONLY mutation path; pure. On success
// with a non-nil event slice or a mutating kind, the engine ledgers the op.
func (s *State) Apply(op *Op, now time.Time) (Result, []Event, error) {
	switch op.Kind {
	case OpRegisterLane:
		return s.applyRegister(op, now)
	case OpResumeLane:
		return s.applyResume(op, now)
	case OpSweep:
		return s.applySweep(op, now)
	case OpMarkDelivered:
		return s.applyMarkDelivered(op, now)
	case OpGrantRole:
		// Admin-only: the engine admits this solely on the human's admin path,
		// so no lane token is consulted and no lane can promote itself.
		return s.applyGrantRole(op, now)
	case OpPruneLane:
		// Admin-only, same path. Closing another lane is a human's call: a lane
		// that crashed cannot close itself, and no agent should be able to
		// evict a peer.
		return s.applyPrune(op, now)
	}

	// Actor ops. Live path: token. Replay path: recorded Lane (engine blanks
	// Lane on ingress, so it cannot be forged).
	l := s.LaneByToken(op.Token)
	if l == nil && op.Token == "" && op.Lane != "" {
		l = s.Lanes[op.Lane]
	}
	if l == nil {
		return nil, nil, ErrBadToken
	}
	if l.Status == StatusClosed {
		return nil, nil, errf("E_LANE_CLOSED", "register a new lane", "lane %s is closed", l.ID)
	}
	op.Lane = l.ID

	// Heartbeat on an active lane touches no replayable state (never
	// ledgered; the engine tracks the ephemeral lease). SPEC §2.
	if op.Kind == OpHeartbeat && l.Status == StatusActive {
		return Result{"ok": true, "lane_id": l.ID}, nil, nil
	}

	var res Result
	var evs []Event
	var err error
	switch op.Kind {
	case OpVouchChild:
		res, evs, err = s.applyVouchChild(l, op)
	case OpLaneOpen:
		res, evs, err = s.applyLaneOpen(l, op, now)
	case OpLaneJoin:
		res, evs, err = s.applyLaneJoin(l, op, now)
	case OpLaneLeave:
		res, evs, err = s.applyLaneLeave(l, op, now)
	case OpLaneSubscribe:
		res, evs, err = s.applyLaneSubscribe(l, op, now)
	case OpLaneExclusive:
		res, evs, err = s.applyLaneExclusive(l, op, now)
	case OpLanePost:
		res, evs, err = s.applyLanePost(l, op, now)
	case OpLaneAnnounce:
		res, evs, err = s.applyLaneAnnounce(l, op, now)
	case OpLaneAck:
		res, evs, err = s.applyLaneAck(l, op, now)
	case OpLaneForceRelease:
		res, evs, err = s.applyLaneForceRelease(l, op, now)
	case OpLaneEvict:
		res, evs, err = s.applyLaneEvict(l, op, now)
	case OpLaneMerge:
		res, evs, err = s.applyLaneMerge(l, op, now)
	case OpLaneClose:
		res, evs, err = s.applyLaneClose(l, op, now)
	case OpLaneAdmit:
		res, evs, err = s.applyLaneAdmit(l, op, now)
	case OpWakeLane:
		res, evs, err = s.applyWake(l)
	case OpActivityCheckpoint:
		res, evs = Result{"ok": true}, []Event{} // state effect: LastCoordination below
	case OpAckBoard:
		res, evs = s.applyAckBoard(l, now)
	case OpUpdateLane:
		if len(op.Description) > s.Limits.MaxDescBytes {
			err = errTooLarge("description", s.Limits.MaxDescBytes)
			break
		}
		l.Description = op.Description
		res, evs = Result{"ok": true}, []Event{{Type: "lane.updated", Lane: l.ID}}
	case OpBindSession:
		// A LEDGERED write, because it is a write.
		//
		// This lived on the engine's read path: BindSession mutated l.SessionID
		// inside e.query, outside any op, so nothing was appended and the binding
		// vanished on restart. The consequence is quiet and bad: lifecycle hooks
		// resolve an agent by session id, and a supplied-but-unmatched id
		// deliberately disables the cwd fallback, so after a restart mail stopped
		// being injected and the claim guard failed open. Nothing reported an
		// error; the wake path simply stopped waking anybody.
		if len(op.SessionID) > s.Limits.MaxNameBytes {
			err = errTooLarge("session_id", s.Limits.MaxNameBytes)
			break
		}
		l.SessionID = op.SessionID
		res = Result{"ok": true, "lane": l.ID, "session_id": l.SessionID}
		evs = []Event{{Type: "lane.updated", Lane: l.ID}}
	case OpCloseLane:
		res, evs = s.applyClose(l, now)
	case OpHeartbeat: // unreachable when sleeping (wake_lane precedes); no-op
		res, evs = Result{"ok": true, "lane_id": l.ID}, nil
	case OpSetSlot:
		res, evs, err = s.applySetSlot(l, op)
	case OpClearSlot:
		res, evs, err = s.applyClearSlot(l, op)
	case OpPutBlob:
		res, evs, err = s.applyPutBlob(l, op, now)
	case OpSendMessage:
		res, evs, err = s.applySend(l, op, now)
	case OpRespond:
		res, evs, err = s.applyRespond(l, op, now)
	case OpAckMessage:
		res, evs, err = s.applyAckMessage(l, op, now)
	case OpClaim:
		res, evs, err = s.applyClaim(l, op, now)
	case OpRelease:
		res, evs, err = s.applyRelease(l, op)
	case OpForceRelease:
		res, evs, err = s.applyForceRelease(l, op)
	default:
		err = errf("E_BAD_OP", "", "unknown op kind %q", op.Kind)
	}
	if err != nil {
		return nil, nil, err
	}
	// Convention: nil events = no state change (no serial, not ledgered);
	// non-nil (possibly empty) = changed. The engine ledgers iff the serial
	// advanced, so the two can never disagree.
	if evs == nil {
		return res, nil, nil
	}
	// Every ledgered actor op refreshes the durable coordination checkpoint.
	l.LastCoordination = now
	// ONE op, ONE serial.
	//
	// Several handlers allocate their own serial because they need it in the
	// result: a lane's key, a join's membership serial, an announcement's id.
	// Finishing again here allocated a SECOND for the same op, and the engine
	// appends at the final value, so the intermediate serial was never written:
	// a permanent hole in the ledger at a point where a real transition had
	// happened.
	//
	// This took a live board down. lane_open advanced the serial by two on every
	// call, and one of the resulting holes held the op that re-created a lane,
	// so on restart the daemon replayed a board where that lane was still closed,
	// hit a close_lane it could not apply, and refused to start. The gap warning
	// at replay had been firing for weeks and reads as cosmetic; it was not.
	//
	// Detected by whether the handler already stamped its events rather than by a
	// flag: finish() writes the serial into every event it stamps, so a non-zero
	// serial on the first one IS the record that this op has been finished.
	if len(evs) == 0 || evs[0].Serial == 0 {
		s.finish(&evs, now)
	}
	return res, evs, nil
}

// finish assigns the op's serial and stamps events.
func (s *State) finish(evs *[]Event, now time.Time) uint64 {
	s.Serial++
	for i := range *evs {
		(*evs)[i].Serial = s.Serial
		(*evs)[i].Sub = i
		(*evs)[i].TS = now
	}
	return s.Serial
}

func dedupKey(lane, id string) string { return lane + "\x00" + id }

// sendDigest binds a send's payload (recipient, type, body, deadline, and every
// attachment handle) so op_id dedup rejects a retry that reused the id with
// different content (SPEC §4).
func sendDigest(op *Op) string {
	parts := []string{op.To, op.MsgType, op.Body, itoa(op.DeadlineSec)}
	for _, a := range op.Attachments {
		parts = append(parts, a.Blob, a.Path, a.Hash)
	}
	return digestOf(parts...)
}

func digestOf(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *State) applyRegister(op *Op, now time.Time) (Result, []Event, error) {
	if len(op.Name) > s.Limits.MaxNameBytes || len(op.Description) > s.Limits.MaxDescBytes ||
		len(op.Nonce) > s.Limits.MaxIDBytes {
		return nil, nil, errTooLarge("name/description/nonce", s.Limits.MaxNameBytes)
	}
	kind := op.LaneKind
	if kind == "" {
		kind = KindEphemeral
	}
	if kind != KindEphemeral && kind != KindPersistent {
		return nil, nil, errf("E_BAD_KIND", "use ephemeral|persistent", "unknown lane kind %q", kind)
	}
	if kind == KindPersistent && op.Nonce == "" {
		return nil, nil, errf("E_BAD_NONCE", "persistent lanes require a client-generated nonce (≥128-bit random); it "+
			"doubles as the resume_lane recovery credential: treat it as a secret", "nonce required for persistent lanes")
	}
	// Response-loss retry: same nonce, lane still active, created within one
	// TTL → return the original result. Outside that window → reattach, or
	// E_NONCE_IN_USE if the nonce is being pointed at a different identity.
	if op.Nonce != "" {
		if id, ok := s.Nonces[op.Nonce]; ok {
			l := s.Lanes[id]
			if l != nil && l.Status == StatusActive && now.Sub(l.LastCoordination) <= s.Limits.LaneTTL && l.CreatedSerial > 0 {
				return Result{
					"lane_id": id, "token": l.Token, "serial": s.Serial,
					"resumed": true, "board": s.Board(),
				}, nil, nil
			}
			// The nonce IS the recovery credential: for every kind of lane, not
			// just persistent ones.
			//
			// This used to refuse and say "use resume_lane", which is the standing
			// -role path and does not apply to an ephemeral lane. So the advice
			// two hundred lines below. "register with a nonce to make recovery
			// require something only you hold": described a credential that could
			// not be used to recover anything. An agent that took the advice, kept
			// its nonce and came back after a restart was told its own nonce was
			// in use, by itself.
			//
			// The damage that caused is worth stating, because it is the whole
			// reason this is a recovery path and not a nicety: the reattach below
			// keys on (name, session_id), and the bridge derives session_id from
			// the harness's process id. That id cannot survive the harness
			// restarting, which is the exact event an agent needs to recover from.
			// Measured on a live fleet: four agents restarted, four re-registered
			// under their own names, and all four became siblings: builder-2,
			// api-a-2, api-b-2, orchestrator-2: with every message addressed to
			// them before the restart stranded in a lane nobody occupied. Nothing
			// looked broken. Every lane was green.
			//
			// A nonce has none of that fragility: the agent chooses it, keeps it,
			// and can present it after anything. It is also a real secret, unlike
			// the (name, session_id) pair, so this path is the SAFER of the two as
			// well as the durable one.
			//
			// Still refused when the name differs. A nonce recovers the identity it
			// was bound to; pointing it at a second name is not recovery, it is two
			// identities sharing one credential, and the honest answer is no.
			if l != nil && l.Name == op.Name {
				l.Token = op.NewToken
				l.LastCoordination = now
				l.Status, l.StaleReason = StatusActive, ""
				l.AckedSerial = 0 // re-arm the awareness gate: this is a new activation
				if op.Agent != nil {
					l.Agent = op.Agent
				}
				if op.PID != 0 {
					l.PID, l.ProcStart = op.PID, op.ProcStart
				}
				if op.SessionID != "" {
					l.SessionID = op.SessionID // the new session owns it now
				}
				// LEDGERED, like every other transition.
				//
				// This branch rotates the token, wakes the lane, re-arms the
				// awareness gate and rebinds the session, and returned no events,
				// so finish() never ran, the serial never advanced, and the engine
				// (which appends only when the serial moves) never wrote it down.
				// Replay therefore never reattached: after a restart the lane was
				// stale again, the freshly issued token did not work, and the OLD
				// token came back to life. A rotated credential returning from the
				// dead is the worst shape this can take, and the documented
				// nonce-recovery path is exactly the case it broke.
				evs := []Event{{Type: "lane.reattached", Lane: l.ID, Data: map[string]any{
					"via": "nonce",
				}}}
				serial := s.finish(&evs, now)
				return Result{
					"lane_id": l.ID, "token": l.Token, "serial": serial,
					"reattached": true, "via": "nonce", "board": s.Board(),
					"session_id": l.SessionID,
				}, evs, nil
			}
			return nil, nil, errf(
				"E_NONCE_IN_USE",
				"that nonce already belongs to lane "+id+", registered under a different name. "+
					"a nonce recovers one identity, so use the name it was bound to, or a new nonce",
				"nonce already bound to lane %s", id,
			)
		}
	}
	// Reattach: a woken agent has no token.
	//
	// A lifecycle hook can tell an agent it has mail, but a fresh turn carries no
	// token, so the agent would register again: getting a SIBLING lane that
	// cannot read or answer the mail addressed to the original. Measured live in
	// opencode: the model read its mail, then failed get_message with E_NO_MESSAGE
	// because it was now a different lane.
	//
	// So a registration presenting both the same session_id AND the same name as a
	// live lane reattaches to it, rotating the token. This is what makes the
	// server's promise that "re-registering after context loss is always safe"
	// literally true.
	//
	// Trust boundary, stated precisely because it was overstated before.
	//
	// session_id is NOT a secret. The bridge derives it from the host's process
	// id (`host-<ppid>`), which any same-user process can enumerate with ps, and
	// the lane's name is on the board. So "name + session_id" is guessable, and
	// presenting both rotates the token: taking the mailbox, the actor
	// identity, and any role the lane holds. Verified against a running daemon:
	// a second registration with a victim's name and session id returned a
	// working token that read the victim's private mail.
	//
	// A lane that registered with a NONCE has a real secret, so that is what
	// reattaches it; session_id alone must not. A lane with only a session_id
	// keeps the old behaviour, because the alternative is that an agent which
	// genuinely lost its context can never recover, and the honest description
	// of that lane is "reclaimable by anyone who learns its session id", which
	// the result now says.
	//
	// What this does NOT fix: every agent shares one coordination secret, so
	// agent-to-agent isolation is a bar to raise, not a wall. See SECURITY.md.
	if op.SessionID != "" && op.Nonce == "" {
		for _, l := range s.Lanes {
			if l.Nonce != "" {
				continue // it has a real credential; a guessable one will not do
			}
			if l.SessionID == op.SessionID && l.Name == op.Name &&
				(l.Status == StatusActive || l.Status == StatusStale) {
				l.Token = op.NewToken
				l.LastCoordination = now
				l.Status, l.StaleReason = StatusActive, ""
				l.AckedSerial = 0 // re-arm the awareness gate: this is a new activation
				if op.Agent != nil {
					l.Agent = op.Agent
				}
				if op.PID != 0 {
					l.PID, l.ProcStart = op.PID, op.ProcStart
				}
				// Ledgered for the same reason as the nonce branch above.
				evs := []Event{{Type: "lane.reattached", Lane: l.ID, Data: map[string]any{
					"via": "session_id",
				}}}
				serial := s.finish(&evs, now)
				return Result{
					"lane_id": l.ID, "token": l.Token, "serial": serial,
					"reattached": true, "via": "session_id", "board": s.Board(),
					"session_id": l.SessionID,
				}, evs, nil
			}
		}
	}

	live, persistent := 0, 0
	for _, l := range s.Lanes {
		if l.Status == StatusActive || l.Sleeping() {
			live++
			if l.Kind == KindPersistent {
				persistent++
			}
		}
	}
	if live >= s.Limits.MaxLanes || (kind == KindPersistent && persistent >= s.Limits.MaxPersistentLanes) {
		return nil, nil, ErrLaneLimit
	}
	id := laneID(s, op.Name)
	// Lineage is claimed and proven separately. An unproven parent is displayed
	// and grants nothing; a vouched one inherits its parent's lanes, skips an
	// exclusive queue, and is exempt from the parent's claims in the guard.
	parentProven := false
	if op.Parent != "" && op.ParentNonce != "" {
		if p := s.Lanes[op.Parent]; p != nil && p.burnChildNonce(op.ParentNonce) {
			parentProven = true
		}
	}
	l := &Lane{
		ID: id, Kind: kind, Name: op.Name, Description: op.Description, Agent: op.Agent,
		PID: op.PID, ProcStart: op.ProcStart, Status: StatusActive, SessionID: op.SessionID,
		Parent:           op.Parent,
		ParentProven:     parentProven,
		LastCoordination: now, Token: op.NewToken, Nonce: op.Nonce,
		Slots: map[string]Slot{},
	}
	s.Lanes[id] = l
	if op.Nonce != "" {
		s.Nonces[op.Nonce] = id
	}
	evs := []Event{{Type: "lane.registered", Lane: id, Data: map[string]any{
		"name": op.Name, "kind": kind, "description": op.Description,
	}}}
	serial := s.finish(&evs, now)
	l.CreatedSerial = serial
	res := Result{
		"lane_id": id, "token": op.NewToken, "serial": serial, "board": s.Board(),
		"gate": "call ack_board to acknowledge the board before set_slot or claim",
	}
	// Hand back the session_id the lane was actually filed under.
	//
	// Reattach keys on (name, session_id), and both halves have to be presentable
	// by the agent for that to be a recovery path rather than a description of
	// one. The name it chose; the session_id it usually did NOT: the bridge
	// supplies it when the model leaves the argument empty, which is almost
	// always. So the agent was told "re-register with the same name and
	// session_id" while holding only one of the two, and there was no call
	// anywhere in the protocol that would tell it the other.
	//
	// Reported from inside the failure by an agent that had just been forked into
	// a sibling: "nothing exposes my session_id to me. If that is an AND, an agent
	// that cannot learn its session_id can never reattach, and the documented
	// recovery path is unreachable in exactly the case it exists for."
	if op.SessionID != "" {
		res["session_id"] = op.SessionID
	}
	// The name was taken, so this agent is addressed as something else.
	//
	// Said out loud because the agent asked for one name, received another, and
	// nothing told it why. A reviewer asked to register as `sol` and was handed
	// `sol-4` across three runs, noticing only because it happened to read the
	// id, and an agent that does not notice publishes the wrong address, tells
	// colleagues to write to `sol`, and never learns why the mail stops.
	//
	// The holder is named along with its state, because that decides what to do
	// about it. A stale or dormant lane still owns its mailbox: taking its name
	// would silently redirect mail meant for somebody else, which is the failure
	// this suffix exists to prevent. So the answer is not to seize the name but
	// to say who has it, and how to become them if they are in fact you.
	if want := slug(op.Name); want != "" && id != want {
		note := "you asked for " + op.Name + " and your id is " + id + ": " + want +
			" is already taken"
		if holder, ok := s.Lanes[want]; ok {
			article := " by a "
			if st := string(holder.Status); st != "" && strings.ContainsRune("aeiou", rune(st[0])) {
				article = " by an "
			}
			note += article + string(holder.Status) + " lane"
			if holder.StaleReason != "" {
				note += " (" + holder.StaleReason + ")"
			}
			// Two different reasons, and saying the wrong one is worse than saying
			// nothing. A stale or dormant lane can still be written to, so handing
			// its name away would redirect somebody else's mail. A retired one
			// cannot receive anything (applySend refuses closed and archived) so
			// it is holding the name purely because its id is stamped into every
			// ledger record that ever named it, and reusing the id would make that
			// history ambiguous. The first is a live conflict; the second is an
			// audit constraint, and an agent reading this can tell which it hit.
			switch holder.Status {
			case StatusClosed, StatusArchived:
				note += ", which is retired and can no longer receive mail: it holds the " +
					"id only because the ledger's history refers to it, and reusing the id " +
					"would make those records mean two different agents"
			default:
				note += ", which still holds its mailbox: a new lane cannot take the name " +
					"without redirecting mail meant for it"
			}
		}
		note += ". Others will address you as " + id + ". If that older lane is YOU, " +
			"reattach instead: register again with the same name and the same nonce, " +
			"or the same name and session_id, and you get the lane and its mail back " +
			"rather than another sibling"
		res["name_note"] = note
	}
	// An id that owes nothing to the name asked for is a surprise, and the agent
	// is the only party that can correct it.
	if op.Name != "" && slug(op.Name) == "" {
		res["name_note"] = "your id is " + id + ", not " + op.Name + ": ids are addresses and " +
			"must be ASCII, and nothing in that name survived. Others will address you as " +
			id + ": register with an ASCII name if you want a meaningful one. Your original " +
			"name is kept and shown to humans on the board."
	}
	// A lane whose only recovery credential is a session id is reclaimable by
	// anyone who learns that id, and the bridge derives it from a process id
	// that any same-user program can enumerate. Say so, rather than letting the
	// word "credential" imply a secret.
	if op.Nonce == "" && op.SessionID != "" {
		res["recovery"] = "this lane can be reclaimed by presenting its name and the session_id above, " +
			"neither of which is secret. AND that session_id will not survive your harness " +
			"restarting, because it names the harness process. To be able to recover after a " +
			"restart, re-register now with a nonce (a random id >=128-bit that you keep): same " +
			"name + same nonce reattaches you to this lane and its mail, after anything."
	}
	if op.Nonce == "" && op.SessionID == "" {
		// With neither recovery credential this lane cannot be reclaimed: lose the
		// token and every message addressed to it becomes unreachable: the agent
		// re-registers, gets a sibling, and cannot answer the mail that woke it.
		// Say so now, while it is still free to fix, rather than at the moment an
		// agent discovers it has been woken into a dead end.
		res["recovery"] = "no session_id or nonce given, so this lane cannot be reclaimed if you " +
			"lose your token: re-registering would create a SECOND lane and this one's mail would " +
			"be unreachable. Re-register with a nonce (a random id >=128-bit that you keep): same " +
			"name + same nonce reattaches you to this lane and its mail, and is the only credential " +
			"that survives your harness restarting."
	}
	// The name was taken, so this lane is a SIBLING of an agent that is already
	// on the board, and mail addressed to that name will never arrive here.
	//
	// This is the one failure the recovery hint above does not cover. Once the
	// bridge started supplying a per-process session_id, that hint stopped firing
	// entirely, yet the trap remained: a one-shot run registers "beta", asks a
	// question, exits; the next run is a NEW process with a NEW session_id, so it
	// registers "beta" again, becomes "beta-2", and finds an empty inbox. Measured
	// end to end with two real opencode agents: the answer was delivered
	// correctly to "beta" and the asker never saw it.
	//
	// Nothing here is wrong at the protocol level: a new session genuinely is a
	// new agent. What was wrong is that it happened in silence. Say it at the
	// moment it happens, and say what mail is being left behind.
	if sib := s.siblingByName(op.Name, id); sib != nil {
		msg := "a lane named " + op.Name + " already exists as " + sib.ID +
			": you are " + id + ", a separate agent. Mail addressed to " + sib.ID +
			" will NOT appear in your inbox."
		if n := len(s.Inbox(sib.ID)); n > 0 {
			msg += " It is holding " + itoa(n) + " message(s) you cannot read."
		}
		res["name_taken"] = msg + " If you ARE that agent returning, you can still get back in:" +
			" register again with the same name and the nonce you kept, and you reattach to it" +
			" instead of forking. If you kept no nonce, that lane is only reachable with its" +
			" session_id: ask a coordinator to lane_merge " + id + " into " + sib.ID +
			", which moves this lane's mail and slots onto that one."
	}
	return res, evs, nil
}

// applyResume is the explicit activation op for standing roles (SPEC §5):
// a complete activation boundary: atomic wake + rotation at one serial.
func (s *State) applyResume(op *Op, now time.Time) (Result, []Event, error) {
	if op.Nonce == "" || op.ResumeID == "" {
		return nil, nil, errf("E_BAD_NONCE", "resume_lane requires nonce and resume_id", "missing nonce or resume_id")
	}
	id, ok := s.Nonces[op.Nonce]
	if !ok {
		return nil, nil, errf("E_BAD_NONCE", "check the nonce; if lost, register a new lane", "unknown nonce")
	}
	l := s.Lanes[id]
	if l == nil || l.Status == StatusArchived {
		return nil, nil, errf("E_NO_LANE", "the lane was archived; register a new one", "lane for nonce is gone")
	}
	if l.Status == StatusClosed {
		return nil, nil, errf("E_LANE_CLOSED", "register a new lane", "lane %s is closed", id)
	}
	// Generation-aware idempotent retry (SPEC §5): same resume_id returns the
	// original token iff the generation is unchanged; else superseded.
	if rec, exists := s.Dedup[dedupKey(id, op.ResumeID)]; exists {
		if rec.Activation == l.Activation {
			return Result{
				"lane_id": id, "token": rec.Token, "activation": l.Activation,
				"serial": s.Serial, "board": s.Board(), "resumed": true,
			}, nil, nil
		}
		return Result{"lane_id": id, "superseded": true, "activation": rec.Activation}, nil, nil
	}
	l.Token = op.NewToken
	l.Activation++
	l.PID, l.ProcStart = op.PID, op.ProcStart
	l.Status, l.StaleReason = StatusActive, ""
	l.StaleSince, l.DormantSince = time.Time{}, time.Time{}
	l.AckedSerial = 0 // gate re-arms per activation
	s.Dedup[dedupKey(id, op.ResumeID)] = &DedupRec{
		Lane: id, ID: op.ResumeID, Activation: l.Activation, Token: op.NewToken, At: now,
	}
	l.LastCoordination = now
	evs := []Event{{Type: "lane.resumed", Lane: id, Data: map[string]any{"activation": l.Activation}}}
	serial := s.finish(&evs, now)
	return Result{
		"lane_id": id, "token": op.NewToken, "activation": l.Activation,
		"serial": serial, "board": s.Board(),
		"gate": "call ack_board before set_slot or claim",
	}, evs, nil
}

// applyWake is the ledgered dormant/stale → active transition (SPEC §2).
//
//nolint:unparam // the (Result, []Event, error) shape is the dispatch contract
func (s *State) applyWake(l *Lane) (Result, []Event, error) {
	if !l.Sleeping() {
		return Result{"ok": true}, nil, nil // no-op: unchanged, not ledgered
	}
	ev := "lane.recovered"
	if l.Status == StatusDormant {
		ev = "lane.awoke"
	}
	l.Status, l.StaleReason = StatusActive, ""
	l.StaleSince, l.DormantSince = time.Time{}, time.Time{}

	// Re-arms the AWARENESS gate: the agent has been away and must look at the
	// board again, but deliberately does NOT start a new credential epoch:
	// Activation is unchanged and the token is not rotated.
	//
	// The two are separate on purpose. A wake happens inside an ordinary op that
	// presented the existing token and has nowhere to return a new one, so
	// rotating here would revoke the caller's credential mid-call. Rotation
	// belongs to register, reattach and resume, which each hand back the new
	// token in their result.
	//
	// Worth naming because "activation" reads as one thing and is two, and
	// SECURITY.md said tokens rotate per activation on the strength of that.
	l.AckedSerial = 0
	return Result{"ok": true}, []Event{{Type: ev, Lane: l.ID}}, nil
}

// applyAckBoard is the atomic checkpoint (SPEC §10): awareness ack + delivery
// transitions of returned pending mail, one op, one serial; snapshot is the
// post-state.
func (s *State) applyAckBoard(l *Lane, _ time.Time) (Result, []Event) {
	evs := []Event{{Type: "board.acked", Lane: l.ID}}
	for _, m := range s.Inbox(l.ID) {
		if m.State == MsgStatePending {
			m.State = MsgStateDelivered
			m.DeliveredAt = s.Serial + 1
			evs = append(evs, Event{
				Type: "message.delivered", Lane: l.ID, To: m.From,
				Data: map[string]any{"msg_serial": m.Serial},
			})
		}
	}
	l.AckedSerial = s.Serial + 1
	return Result{
		"ok": true, "acked_serial": s.Serial + 1, "serial": s.Serial + 1,
		// Both names for the same mail: the inbox tool calls this `messages` and
		// this call named it `inbox`, each using the other's name, so an agent
		// reading the obvious key from either one got an empty list. See
		// Engine.Inbox.
		// The board is stamped with the serial this op WILL have, not the one
		// before it.
		//
		// Apply is the fold: the result is built while s.Serial still holds the
		// pre-op value, and the common finish path advances it afterwards. So
		// every cursor field here says s.Serial+1 while the embedded board said
		// s.Serial: an atomic checkpoint that disagreed with itself by one, and
		// SPEC.md promises a COHERENT post-state snapshot. A client treating
		// board.serial as the cut, which is the obvious reading, would re-fetch
		// one event it already held or reason from a board a serial behind its
		// own cursor.
		"board": s.boardAtNextSerial(), "inbox": s.Inbox(l.ID), "messages": s.Inbox(l.ID),
		"truncated_before_serial": l.TruncatedBefore,
		"announcements":           s.UnackedFor(l.ID),
	}, evs
}

// applyPrune closes lanes the human has finished with. Reaching a dead lane is
// otherwise impossible: close_lane needs the lane's own token, and a lane that
// crashed or lost its context no longer has one, so without this the board
// accumulates debris nobody can clear.
//
// op.To names a single lane; empty means "every lane that is not live", which is
// the common case after a day's work.
func (s *State) applyPrune(op *Op, now time.Time) (Result, []Event, error) {
	var targets []*Lane
	if op.To != "" {
		l := s.Lanes[op.To]
		if l == nil {
			return nil, nil, errf("E_NO_LANE", "check the id on the board", "no lane %q", op.To)
		}
		targets = append(targets, l)
	} else {
		// Sorted, because the events below go into the ledger in this order.
		// Ranging the map directly gave a different audit sequence every run.
		for _, id := range sortedKeys(s.Lanes) {
			l := s.Lanes[id]
			// Never prune a lane that is still working: only the debris.
			if l.Status != StatusActive && l.Status != StatusClosed {
				targets = append(targets, l)
			}
		}
	}
	var evs []Event
	var ids []string
	for _, l := range targets {
		r, e := s.applyClose(l, now)
		_ = r
		evs = append(evs, e...)
		ids = append(ids, l.ID)
	}
	slices.Sort(ids)
	// LEDGERED. applyPrune closes lanes, blanks their tokens and releases their
	// claims, and it returned without finish(), so the serial never moved, the
	// engine never appended, and replay undid all of it. The human was told the
	// prune succeeded; after the next restart the lanes were back, stale rather
	// than closed, holding their old tokens again.
	//
	// It reaches this point through the special-op switch, which is why it
	// escaped the finishing path every ordinary op goes through.
	serial := s.finish(&evs, now)
	return Result{"ok": true, "pruned": ids, "count": len(ids), "serial": serial}, evs, nil
}

func (s *State) applyClose(l *Lane, now time.Time) (Result, []Event) {
	l.Status = StatusClosed
	l.Token = ""
	released := s.releaseClaims(l.ID)
	evs := []Event{{Type: "lane.closed", Lane: l.ID}}
	evs = append(evs, s.departAllChannels(l.ID)...)
	for _, p := range released {
		evs = append(evs, Event{Type: "claim.released", Lane: l.ID, Data: map[string]any{"path": p}})
	}
	evs = append(evs, s.strandedQuestions(l, now)...)
	return Result{"ok": true, "released_claims": len(released)}, evs
}

// strandedQuestions terminates the questions a departing lane will never answer.
//
// The sweep already decides this correctly, and has a branch and a sentence
// written for exactly this case. "recipient closed its lane before answering
// … nobody will answer this now". It just does not run until the DEADLINE. So
// an agent that asked a question with a ten-minute deadline waited the whole
// ten minutes for an answer that became impossible in the first second, while
// the board knew: a closed lane cannot resume (resume_lane returns
// E_LANE_CLOSED) and Gone() is documented as "never comes back".
//
// Ten minutes of an agent blocking on a certainty is the cost, and it is paid
// by the one participant who did nothing wrong.
func (s *State) strandedQuestions(gone *Lane, now time.Time) []Event {
	var evs []Event
	// Sorted: these events go into the ledger in this order.
	for _, serial := range sortedKeys(s.Messages) {
		m := s.Messages[serial]
		if m.To != gone.ID || m.Terminal() || !m.Expecting() {
			continue
		}
		m.State = MsgStateExpiredDead
		m.ExpireDetail = "recipient closed its lane before answering; it finished deliberately " +
			"and released its claims, so this is not a crash and there is nothing of its to " +
			"verify: nobody will answer this now"
		m.TerminalAt = now
		evs = append(evs, Event{
			Type: "message." + m.State, Lane: m.To, To: m.From,
			Data: map[string]any{"msg_serial": m.Serial, "detail": m.ExpireDetail},
		})
	}
	return evs
}

func (s *State) applySetSlot(l *Lane, op *Op) (Result, []Event, error) {
	if l.AckedSerial == 0 {
		return nil, nil, ErrMustAck
	}
	if len(op.Text) > s.Limits.MaxBodyBytes || len(op.Dirs) > s.Limits.MaxDirs {
		return nil, nil, errTooLarge("slot text/dirs", s.Limits.MaxBodyBytes)
	}
	id := op.SlotID
	if id == "" {
		// The next FREE id, not len+1. Counting produced a collision the moment
		// a middle slot was cleared: with s1,s2,s3 and s2 gone, len is 2 and the
		// next id is "s3", which already exists, so the new declaration
		// silently overwrote the old one and the limit check waved it through
		// because the id was not new. For a lane that has never cleared a slot
		// this generates exactly the ids it always did.
		for n := len(l.Slots) + 1; ; n++ {
			id = "s" + itoa(n)
			if _, taken := l.Slots[id]; !taken {
				break
			}
		}
	}
	if _, exists := l.Slots[id]; !exists && len(l.Slots) >= s.Limits.MaxSlotsPerLane {
		return nil, nil, errf("E_SLOT_LIMIT", "clear_slot an old slot first", "lane has %d slots (max)", len(l.Slots))
	}
	if len(op.Refs) > s.Limits.MaxDirs {
		return nil, nil, errTooLarge("refs", s.Limits.MaxDirs)
	}
	// Declaration-time overlap detection (the duplicate-objective fix). Purely advisory: the
	// slot is always set (we never block someone from declaring work) but
	// BOTH sides learn immediately that two lanes intend the same scope, which
	// is the earliest honest moment to catch duplicated effort.
	overlaps := s.overlapsFor(op.Refs, op.Dirs, l.ID)
	l.Slots[id] = Slot{
		ID: id, Text: op.Text, Dirs: op.Dirs, Refs: op.Refs,
		Activity: op.Activity, Holds: op.Holds,
		// Recorded, never recomputed: replay must reconstruct the same footprint
		// rather than re-scoring against a reindexed repository.
		Predicted:     op.Predicted,
		UpdatedSerial: s.Serial + 1,
	}

	evs := []Event{{Type: "slot.set", Lane: l.ID, Data: map[string]any{
		"slot_id": id, "text": op.Text, "dirs": op.Dirs, "refs": op.Refs,
		"overlaps": len(overlaps),
	}}}
	// An agent updating its focus naturally calls set_slot again with new text
	// and no slot_id, which MINTS A SLOT every time, so a lane that is simply
	// working stacks declarations until it hits the cap and starts erroring. The
	// tool cannot know whether a second slot was intended, so it does not
	// guess: it says what it did and what to pass to update instead. Told, not
	// prevented.
	grew := op.SlotID == "" && len(l.Slots) > 1
	// Tell the incumbents too: an overlap only one side can see is how the
	// measured collision survived for days.
	notified := map[string]bool{}
	strong := 0
	for _, o := range overlaps {
		if o.Strong() {
			strong++
		}
		if notified[o.Lane] {
			continue
		}
		notified[o.Lane] = true
		evs = append(evs, Event{
			Type: "slot.overlap_noted", Lane: l.ID, To: o.Lane,
			Data: map[string]any{"slot_id": id, "text": op.Text, "signal": o.Signal},
		})
	}
	res := Result{"ok": true, "slot_id": id}
	if grew {
		res["note"] = "this ADDED a slot (" + id + "); your lane now declares " +
			itoa(len(l.Slots)) + ". If you meant to change what you are doing rather than " +
			"take on something additional, pass slot_id=\"" + id + "\" next time, or clear_slot the " +
			"ones you have finished: a lane declaring five things is read as doing five things."
	}
	if len(overlaps) > 0 {
		res["overlaps"] = overlaps
	}
	if strong > 0 {
		res["warning"] = "another lane is already pursuing the same objective: you are probably " +
			"about to duplicate its work. Read its slot, then message it (question/handoff) to " +
			"split or stand down. This is the measured failure; do not just proceed."
	} else if len(overlaps) > 0 {
		res["note"] = "other lanes are active on these paths. Concurrent edits are normal: this is " +
			"awareness, not a conflict. Coordinate only if your changes are semantically incompatible."
	}
	return res, evs, nil
}

func (s *State) applyClearSlot(l *Lane, op *Op) (Result, []Event, error) {
	if _, ok := l.Slots[op.SlotID]; !ok {
		return nil, nil, errf("E_NO_SLOT", "list your slots via the board", "no slot %q", op.SlotID)
	}
	delete(l.Slots, op.SlotID)
	return Result{"ok": true},
		[]Event{{Type: "slot.cleared", Lane: l.ID, Data: map[string]any{"slot_id": op.SlotID}}}, nil
}

// nearestLanesHint lists live lanes, closest-looking first, so a misaddressed
// message can be fixed in one step instead of a board round trip.
func nearestLanesHint(s *State, want string) string {
	var near, live []string
	w := strings.ToLower(want)
	for id, l := range s.Lanes {
		if l.Status != StatusActive && !l.Sleeping() {
			continue
		}
		switch {
		case strings.Contains(strings.ToLower(id), w), strings.Contains(w, strings.ToLower(id)),
			strings.Contains(strings.ToLower(l.Name), w):
			near = append(near, id)
		default:
			live = append(live, id)
		}
	}
	sort.Strings(near)
	sort.Strings(live)
	if len(near) > 0 {
		return "no lane " + want + ": did you mean " + strings.Join(near, ", ") + "?"
	}
	if len(live) == 0 {
		return "no lane " + want + ", and no other lane is live either"
	}
	if len(live) > 8 {
		live = live[:8]
	}
	return "no lane " + want + ": live lanes are: " + strings.Join(live, ", ") +
		operatorFallback(s)
}

// operatorFallback names the human's own lane, because it is the one address
// that is always there.
//
// An agent that finishes work and wants to report it can find the recipient
// gone: lanes are reaped, and the agent that asked for the work may be the one
// that ended. A reviewer hit exactly this: its report was addressed to a lane
// that had been reaped, the refusal listed live lanes, and it concluded there was
// no durable delivery path at all. It then tried broadcast, which is
// coordinator-only and correctly refused, and the review survived only in its own
// stdout.
//
// There WAS a path and nothing pointed at it. The operator's lane is persistent,
// exists as soon as anyone opens the board, and belongs to the one participant
// who always wants to know. It was already in that list of live lanes, spelled
// like any other agent, with nothing to say it was the person.
//
// Named only when it is not the lane being looked for, and only when it exists,
// on a board no human has opened there is nothing to offer, and inventing one
// would be worse than the silence.
func operatorFallback(s *State) string {
	for id, l := range s.Lanes {
		if l.Agent == nil || l.Agent.Surface != "web" {
			continue
		}
		if l.Status == StatusClosed || l.Status == StatusArchived {
			continue
		}
		return ". If this was a report for a person rather than an agent, the operator " +
			"is on this board as " + id + ": that lane is persistent and outlives the " +
			"agents, so it is the address that keeps working when the one you wanted is gone"
	}
	return ""
}

func (s *State) applySend(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	to, ok := s.Lanes[op.To]
	if !ok || to.Status == StatusClosed || to.Status == StatusArchived {
		// Name the candidates. "Check the board" is advice the agent has to act
		// on with another call, and it already told us who it meant: an agent
		// that addressed "claude" and was told to go looking gave up instead,
		// rather than guessing which of the live lanes was the right one.
		return nil, nil, errf("E_NO_LANE", nearestLanesHint(s, op.To), "no live lane %q", op.To)
	}
	switch op.MsgType {
	case MsgNotify, MsgQuestion, MsgRequest, MsgHandoff:
	default:
		return nil, nil, errf("E_BAD_TYPE", "use notify|question|request|handoff", "unknown message type %q", op.MsgType)
	}
	if len(op.Body) > s.Limits.MaxBodyBytes || len(op.OpID) > s.Limits.MaxIDBytes {
		return nil, nil, errTooLarge("body/op_id", s.Limits.MaxBodyBytes)
	}
	atts, err := s.validateAttachments(l, op.Attachments)
	if err != nil {
		return nil, nil, err
	}
	op.Attachments = atts
	// Identified-op dedup (SPEC §4): digest-bound, effectively-once within
	// the lesser of the window and the per-lane cap.
	digest := sendDigest(op)
	if op.OpID != "" {
		if rec, exists := s.Dedup[dedupKey(l.ID, op.OpID)]; exists {
			if rec.Digest != digest {
				return nil, nil, errf("E_OP_ID_CONFLICT", "op_id was already used with a different payload; generate a "+
					"fresh op_id", "op_id reuse with different request")
			}
			return Result{"ok": true, "msg_serial": rec.Serial, "deduplicated": true}, nil, nil
		}
	}
	var displaced *Message
	if nonTerminalCount(s, to.ID) >= s.Limits.MaxMailboxDepth {
		// A notify may displace the oldest displaceable notify; nothing
		// expecting an answer is ever displaced (SPEC §8).
		displaced = s.oldestDisplaceableNotify(to.ID)
		if op.MsgType != MsgNotify || displaced == nil {
			return nil, nil, ErrMailboxFull
		}
		displaced.State = MsgStateDisplaced
		displaced.TerminalAt = now
	}
	return s.finishSend(l, to, op, now, digest, displaced)
}

func (s *State) finishSend(
	l, to *Lane, op *Op, now time.Time, digest string, displaced *Message,
) (Result, []Event, error) {
	expecting := op.MsgType == MsgQuestion || op.MsgType == MsgRequest
	var deadline time.Time
	if expecting {
		maxD := s.Limits.MaxDeadline
		if to.Kind == KindPersistent {
			maxD = s.Limits.MaxDeadlineDormant // dormancy-aware ceiling
		}
		d := s.Limits.DefaultDeadline
		if op.DeadlineSec > 0 {
			// Clamp in SECONDS, before converting to a Duration.
			//
			// The multiply happened first, so a large positive deadline_s
			// overflowed int64 nanoseconds and came out NEGATIVE: min() then
			// happily chose it over the ceiling, and the wire accepted MaxInt64
			// and returned a deadline one second in the PAST. The next sweep
			// expires a question that was just asked, which reads to the sender
			// as an agent that ignored them.
			//
			// Comparing seconds first cannot overflow: maxD is hours, so the
			// bound fits in an int with room to spare.
			maxSec := int(maxD / time.Second)
			if op.DeadlineSec < maxSec {
				d = time.Duration(op.DeadlineSec) * time.Second
			} else {
				d = maxD
			}
		}
		deadline = now.Add(d)
	}
	serial := s.Serial + 1
	m := &Message{
		Serial: serial, From: l.ID, To: to.ID, Type: op.MsgType, Body: op.Body,
		State: MsgStatePending, Deadline: deadline, Attachments: op.Attachments,
		SentAt: now,
	}
	s.Messages[serial] = m
	evs := []Event{{Type: "message.sent", Lane: l.ID, To: to.ID, Data: map[string]any{
		"msg_type": op.MsgType, "from": l.ID, "attachments": len(op.Attachments),
	}}}
	if displaced != nil {
		evs = append(evs, Event{
			Type: "message.displaced", Lane: to.ID, To: displaced.From,
			Data: map[string]any{"msg_serial": displaced.Serial},
		})
	}
	if op.OpID != "" {
		s.Dedup[dedupKey(l.ID, op.OpID)] = &DedupRec{
			Lane: l.ID, ID: op.OpID, Digest: digest, Serial: serial,
			Activation: l.Activation, At: now,
		}
	}
	res := Result{"ok": true, "msg_serial": serial}
	if expecting {
		res["deadline"] = deadline
	}
	if to.Sleeping() {
		// A sleeping lane that has been SUPERSEDED will never wake, so the
		// reassurance below is false for it and has to be said differently.
		//
		// This is the case that lost two full bug reports on a live fleet. A
		// restart forked every lane; agents addressed mail to the names they knew;
		// those lanes were dormant tombstones whose occupants were now alive under
		// `-2` ids. Lanes accepted the mail and told the senders it would be seen
		// "when it next wakes". Nobody was coming. The failure was invisible from
		// both ends at once: the sender read success, the intended recipient saw
		// nothing, and it took a third channel to notice.
		if live := s.liveSiblingOf(to); live != nil {
			res["note"] = "delivered to " + to.ID + ", which is " + string(to.Status) +
				", but " + live.ID + " is LIVE under the same name and is almost certainly who " +
				"you meant. " + to.ID + " will not wake to read this. Resend to " + live.ID + "."
		} else {
			// The message is already committed by the time we get here, so the note
			// must say what IS true, not warn about what might happen: it is queued
			// and will be delivered; only the deadline is at risk.
			res["note"] = "delivered to " + to.ID + ", which is currently " + string(to.Status) +
				": it will see this when it next wakes. The message is not lost; only the response " +
				"deadline is at risk, so re-send with a larger deadline_s if you need an answer, or " +
				"use notify/handoff when you do not."
		}
	}
	return res, evs, nil
}

// liveSiblingOf finds an active lane sharing this one's name.
//
// Deliberately NOT siblingByName, which ranks by how much mail a lane holds
// because it answers a different question. "which mailbox can the caller not
// read". Here the only thing that matters is which sibling is ALIVE, and
// borrowing that ranking would quietly skip a live lane that happened to hold
// less mail than a dead one.
func (s *State) liveSiblingOf(to *Lane) *Lane {
	for _, l := range s.Lanes {
		if l.ID != to.ID && l.Name == to.Name && l.Status == StatusActive {
			return l
		}
	}
	return nil
}

func (s *State) oldestDisplaceableNotify(lane string) *Message {
	var oldest *Message
	for _, m := range s.Messages {
		if m.To == lane && m.Type == MsgNotify &&
			(m.State == MsgStatePending || m.State == MsgStateDelivered) {
			if oldest == nil || m.Serial < oldest.Serial {
				oldest = m
			}
		}
	}
	return oldest
}

func (s *State) applyRespond(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	m, ok := s.Messages[op.MsgSerial]
	if !ok || m.To != l.ID {
		return nil, nil, errf("E_NO_MESSAGE", "check your inbox", "no message %d addressed to you", op.MsgSerial)
	}
	if m.Terminal() {
		return nil, nil, errf("E_MSG_FINAL", "", "message %d already %s", m.Serial, m.State)
	}
	if len(op.Body) > s.Limits.MaxBodyBytes {
		return nil, nil, errTooLarge("response body", s.Limits.MaxBodyBytes)
	}
	var st string
	switch op.Disposition {
	case "answer":
		if m.Type != MsgQuestion {
			return nil, nil, errf("E_BAD_DISPOSITION", "only questions take 'answer'", "cannot answer a %s", m.Type)
		}
		st = MsgStateAnswered
	case "approve", "deny":
		if m.Type != MsgRequest {
			return nil, nil, errf(
				"E_BAD_DISPOSITION", "only requests take approve|deny", "cannot %s a %s", op.Disposition, m.Type,
			)
		}
		st = map[string]string{"approve": MsgStateApproved, "deny": MsgStateDenied}[op.Disposition]
	case "decline":
		if !m.Expecting() {
			return nil, nil, errf(
				"E_BAD_DISPOSITION", "notify/handoff take ack_message, not decline", "cannot decline a %s", m.Type,
			)
		}
		st = MsgStateDeclined
	default:
		return nil, nil, errf(
			"E_BAD_DISPOSITION", "use answer|approve|deny|decline", "unknown disposition %q", op.Disposition,
		)
	}
	m.State = st
	m.Response = op.Body
	m.Consumed = true // responding proves receipt (SPEC §8)
	m.TerminalAt = now
	m.RespondedAt = s.Serial + 1
	res := Result{"ok": true, "state": st}
	// Say when the answer has nowhere to go.
	//
	// An agent that asked a question can close its lane while the answer is
	// being composed, and a closed lane never comes back. The response is
	// recorded, the event is addressed to a lane that will never read it, and
	// this returned a bare {"ok": true}: a confident, specific and false
	// statement of the kind that costs the next agent an hour. It cannot be
	// prevented (the asker left mid-thought, which is allowed) but it can be
	// reported, so the responder stops waiting for a follow-up and does not
	// treat the exchange as closed by agreement.
	if asker := s.Lanes[m.From]; asker.Gone() {
		res["delivered"] = false
		res["note"] = "recorded, but " + m.From + " closed its lane before this arrived. " +
			"nobody will read this answer, and no follow-up is coming"
	}
	return res,
		[]Event{{Type: "message." + st, Lane: l.ID, To: m.From, Data: map[string]any{
			"msg_serial": m.Serial,
		}}}, nil
}

// applyAckMessage: pending/delivered → acked (terminal + consumed for
// notify/handoff); on already-terminal mail it is the consumption transition
// (SPEC §8).
func (s *State) applyAckMessage(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	m, ok := s.Messages[op.MsgSerial]
	if !ok || m.To != l.ID {
		return nil, nil, errf("E_NO_MESSAGE", "", "no message %d addressed to you", op.MsgSerial)
	}
	if m.Terminal() {
		if m.Consumed {
			return Result{"ok": true, "state": m.State, "consumed": true}, nil, nil
		}
		m.Consumed = true
		return Result{"ok": true, "state": m.State, "consumed": true},
			[]Event{{
				Type: "message.consumed", Lane: l.ID, To: m.From,
				Data: map[string]any{"msg_serial": m.Serial},
			}}, nil
	}
	m.State = MsgStateAcked
	m.AckedAt = s.Serial + 1
	if !m.Expecting() { // acked is terminal + consumed for notify/handoff
		m.Consumed = true
		m.TerminalAt = now
	}
	return Result{"ok": true, "state": MsgStateAcked},
		[]Event{{Type: "message.acked", Lane: l.ID, To: m.From, Data: map[string]any{"msg_serial": m.Serial}}}, nil
}

func (s *State) applyClaim(l *Lane, op *Op, now time.Time) (Result, []Event, error) {
	if l.AckedSerial == 0 {
		return nil, nil, ErrMustAck
	}
	if op.Mode != ClaimShared && op.Mode != ClaimExclusive {
		return nil, nil, errf("E_BAD_MODE", "use shared|exclusive", "unknown claim mode %q", op.Mode)
	}
	if len(op.Path) > s.Limits.MaxPathBytes || len(op.Note) > s.Limits.MaxNoteBytes {
		return nil, nil, errTooLarge("path/note", s.Limits.MaxPathBytes)
	}
	path := cleanPath(op.Path)
	overlaps := s.overlapping(path, l.ID)
	// SPEC §9 matrix: exclusive refused on ANY overlap; shared refused only
	// under exclusive.
	granted := true
	if op.Mode == ClaimExclusive && len(overlaps) > 0 {
		granted = false
	} else {
		for _, c := range overlaps {
			if c.Mode == ClaimExclusive {
				granted = false
				break
			}
		}
	}
	ov := make([]map[string]any, 0, len(overlaps))
	for _, c := range overlaps {
		ov = append(ov, map[string]any{"lane": c.Lane, "path": c.Path, "mode": c.Mode, "note": c.Note})
	}
	if !granted {
		return Result{"granted": false, "overlaps": ov},
			[]Event{{Type: "claim.conflict_noted", Lane: l.ID, Data: map[string]any{"path": path, "mode": op.Mode}}}, nil
	}
	for _, c := range s.Claims {
		if c.Lane == l.ID && c.Path == path { // renewal (ledgered: drives expiry)
			c.Renewed, c.Mode, c.Note = now, op.Mode, op.Note
			return Result{"granted": true, "renewed": true, "overlaps": ov},
				[]Event{{Type: "claim.renewed", Lane: l.ID, Data: map[string]any{"path": path, "mode": op.Mode}}}, nil
		}
	}
	mine, total := 0, len(s.Claims)
	for _, c := range s.Claims {
		if c.Lane == l.ID {
			mine++
		}
	}
	if mine >= s.Limits.MaxClaimsPerLane || total >= s.Limits.MaxClaimsGlobal {
		return nil, nil, errf("E_CLAIM_LIMIT", "release claims you no longer need", "claim limit reached (%d/lane, %d "+
			"global)", s.Limits.MaxClaimsPerLane, s.Limits.MaxClaimsGlobal)
	}
	cl := &Claim{Lane: l.ID, Path: path, Mode: op.Mode, Note: op.Note, Acquired: now, Renewed: now}
	s.Claims = append(s.Claims, cl)
	cl.AcquiredSerial = s.Serial + 1
	return Result{"granted": true, "overlaps": ov},
		[]Event{{Type: "claim.acquired", Lane: l.ID, Data: map[string]any{
			"path": path, "mode": op.Mode, "note": op.Note,
		}}}, nil
}

func (s *State) applyRelease(l *Lane, op *Op) (Result, []Event, error) {
	path := cleanPath(op.Path)
	for i, c := range s.Claims {
		if c.Lane == l.ID && c.Path == path {
			s.Claims = append(s.Claims[:i], s.Claims[i+1:]...)
			return Result{"ok": true},
				[]Event{{Type: "claim.released", Lane: l.ID, Data: map[string]any{"path": path}}}, nil
		}
	}
	return nil, nil, errf("E_NO_CLAIM", "list claims via the board", "no claim on %q", path)
}

// applyMarkDelivered: ledgered pending→delivered receipts, idempotent.
func (s *State) applyMarkDelivered(op *Op, now time.Time) (Result, []Event, error) {
	var evs []Event
	for _, serial := range op.MsgSerials {
		m, ok := s.Messages[serial]
		if !ok || m.State != MsgStatePending {
			continue
		}
		m.State = MsgStateDelivered
		m.DeliveredAt = s.Serial + 1
		// `now` is the ledgered timestamp on replay, so this reconstructs
		// identically rather than stamping the moment of replay.
		m.DeliveredTime = now
		evs = append(evs, Event{
			Type: "message.delivered", Lane: m.To, To: m.From,
			Data: map[string]any{"msg_serial": m.Serial},
		})
	}
	if len(evs) == 0 {
		return Result{"changed": false}, nil, nil
	}
	s.finish(&evs, now)
	return Result{"changed": true}, evs, nil
}

// boardAtNextSerial is Board() stamped with the serial this op is about to take.
//
// Separate from Board() because every OTHER caller is reading committed state,
// where s.Serial is already correct. Only a result built mid-fold has to
// anticipate, and doing it here rather than inside Board() keeps the anticipation
// visible at the one call site that needs it.
func (s *State) boardAtNextSerial() map[string]any {
	b := s.Board()
	b["serial"] = s.Serial + 1
	return b
}
