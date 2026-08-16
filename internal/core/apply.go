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
	// A choice is a button label, so it is bounded as a name and there are few of
	// them. Bounded here rather than in Apply for the reason at the top of this
	// function: these strings are already in ledgers on disk, and a rule added to
	// the fold is retroactive.
	if len(op.Choices) > MaxChoices {
		return errTooLarge("choices", MaxChoices)
	}
	if err := boundStrings(lim.MaxNameBytes, "choices", op.Choices); err != nil {
		return err
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
		// register was then refused, so the agent could not coordinate AT
		// ALL, over a descriptive field. Relaxing an admission bound is safe in
		// the direction that matters: Admit runs only on ingress, so nothing
		// already in a ledger becomes inadmissible.
		for field, v := range map[string]string{
			"agent.cwd": a.CWD, "agent.repo_dir": a.RepoDir,
			"agent.repo_remote": a.RepoRemote, "agent.repo_roots": a.RepoRoots,
		} {
			if len(v) > lim.MaxPathBytes {
				return errTooLarge(field, lim.MaxPathBytes)
			}
		}
	}
	switch op.Kind {
	case OpSpaceAnnounce:
		// An announcement with nothing in it obliges every member to
		// acknowledge nothing, and re-pings them until they do. The UPPER bound
		// on a body was checked and the lower one was not.
		//
		// Not hypothetical: a whole coordination space between two agents ran
		// on empty announcements, because the caller sent the text under the
		// wrong key and the missing value became "". Each returned a serial and
		// a must_ack count, so it looked delivered from the sending side, while
		// the receiving agent saw an agent full of obligations that said nothing
		// and had to ask a human what was going on.
		if strings.TrimSpace(op.Body) == "" {
			return errf("E_EMPTY_BODY",
				"pass the announcement text as `body`",
				"`body` is empty: an announcement needs something to say, because it "+
					"obliges every member to acknowledge it, and an empty one obliges "+
					"them to acknowledge nothing")
		}
	case OpSpacePost:
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

	// ClaimVerified records that the engine checked a coordinator claim against
	// the daemon's own data directory. An impure input, so the VERDICT is
	// recorded rather than the secret, and replay applies the same decision
	// without reading a file that may since have been consumed (SPEC §2, §4).
	// Blanked on ingress like AgentID: an agent cannot assert it.
	ClaimVerified bool `json:"claim_verified,omitempty"`

	// AdoptAuthorised records that the ENGINE checked the caller may take over
	// an abandoned mailbox: the human proven present at this machine, or an
	// agent the operator promoted. Same rule as ClaimVerified: an impure
	// authorisation decision is made once, at ingress, and the VERDICT is
	// recorded so replay reaches the same answer without re-deciding it. Blanked
	// on ingress like AgentID, so an agent cannot assert it.
	AdoptAuthorised bool `json:"adopt_authorised,omitempty"`

	// Actor resolution. Token authenticates (live path); Agent is set by Apply
	// and used on replay (the engine blanks it on ingress: unforgeable).
	Token   string `json:"-"`
	AgentID string `json:"agent_id,omitempty"`

	// register / resume / update
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	PID         int    `json:"pid,omitempty"`
	// NoProcess says this participant HAS no process, which is different from
	// omitting a pid.
	//
	// An omitted pid means "unchanged", so an agent that reattaches without one
	// keeps whatever it had: that rule protects an agent whose harness does not
	// know its own pid, and it is right. It leaves no way to say that a pid
	// recorded earlier was wrong, and the human at the board is exactly that
	// case. Their agent was registered with the DAEMON's pid, so after a
	// restart the sweep probed a dead process and reported a person as
	// `process_exited`, which is both false and a grim thing to say about
	// somebody who is simply not typing.
	//
	// A person's liveness is silence, not a process table entry.
	NoProcess bool `json:"no_process,omitempty"`
	// Choices enumerates the answers a question will accept, so the answer space
	// is stated by whoever knows it rather than guessed by whoever reads it.
	Choices   []string   `json:"choices,omitempty"`
	ProcStart int64      `json:"proc_start,omitempty"`
	NewToken  string     `json:"token,omitempty"` // engine-generated; encrypted at rest
	Nonce     string     `json:"nonce,omitempty"` // encrypted at rest
	ResumeID  string     `json:"resume_id,omitempty"`
	SessionID string     `json:"session_id,omitempty"` // harness session, for hook lookup
	Agent     *AgentInfo `json:"agent,omitempty"`      // who is behind the agent (descriptive only)
	Parent    string     `json:"parent,omitempty"`     // the agent that spawned this one (§8.2)
	// ParentNonce is the one-time secret the parent issued for this child.
	//
	// Parent alone is a claim anyone can make; this is the proof. A parent that
	// actually spawned a child can hand it a secret: same process, same trust
	// domain, and nobody else has it.
	ParentNonce string    `json:"parent_nonce,omitempty"`
	AgentKind   AgentKind `json:"agent_kind,omitempty"`

	// declare / undeclare
	SlotID string   `json:"slot_id,omitempty"`
	Text   string   `json:"text,omitempty"`
	Dirs   []string `json:"dirs,omitempty"`
	Refs   []string `json:"refs,omitempty"` // objective ids: pr:1186, gate:typos …
	// Activity is the ROLE this agent has on the work (implement, review, test).
	// Holds are exclusive host resources it needs (port:8080, lock:.git/index).
	Activity string   `json:"activity,omitempty"`
	Holds    []string `json:"holds,omitempty"`

	// send / respond / ack
	To          string       `json:"to,omitempty"`
	MsgType     string       `json:"msg_type,omitempty"`
	Body        string       `json:"body,omitempty"` // encrypted at rest
	DeadlineSec int          `json:"deadline_sec,omitempty"`
	OpID        string       `json:"op_id,omitempty"`
	MsgSerial   uint64       `json:"msg_serial,omitempty"`
	Disposition string       `json:"disposition,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"` // send (A2)

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
	DeadAgents     []string `json:"dead_agents,omitempty"`
	StaleAgents    []string `json:"stale_agents,omitempty"`
	AlivePIDs      []int    `json:"alive_pids,omitempty"`

	// mark_delivered: ledgered pending→delivered receipts
	MsgSerials []uint64 `json:"msg_serials,omitempty"`

	// Spaces (SPEC-CHANNELS.md). "Space" is the Go name; the wire name is
	// "agent", which is the vocabulary the protocol and the spec both use.
	Space     string `json:"space,omitempty"`
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
	// is: it decides agent membership, and recomputing it on replay reconstructs
	// a different fleet.
	Predicted []PredFile `json:"predicted,omitempty"`
}

// Op kinds.
const (
	OpRegister           = "register"
	OpResume             = "resume"
	OpWake               = "wake"
	OpActivityCheckpoint = "activity_checkpoint"
	OpAckBoard           = "check_in"
	OpUpdate             = "update"
	OpBindSession        = "bind_session"
	OpSignOff            = "sign_off"
	OpHeartbeat          = "heartbeat"
	OpSetSlot            = "declare"
	OpClearSlot          = "undeclare"
	OpSendMessage        = "send"
	OpRespond            = "respond"
	OpAckMessage         = "ack"
	OpClaim              = "claim"
	OpRelease            = "release"
	OpSweep              = "sweep"
	OpMarkDelivered      = "mark_delivered"
	OpPutBlob            = "put_blob"
	OpGrantRole          = "grant_role"
	OpPrune              = "prune"
	// OpPruneOwn is an agent tidying up after ITSELF: its own record, or a
	// child it vouched for. A new kind rather than a token-bearing prune,
	// because the ownership rule below has to live in Apply (it depends on
	// state, which Admit cannot see) and a rule added to Apply is retroactive:
	// it would be applied to ops that older code already accepted. No ledger
	// anywhere contains this kind, so there is nothing to be retroactive about.
	OpPruneOwn = "prune_own"
	// OpAdoptAgent moves an abandoned agent's mail onto a live one.
	//
	// An agent that registered with neither a nonce nor a session id cannot be
	// reattached by anybody, ever: both recovery paths key on one of those. Its
	// mailbox then keeps accepting mail that no one can read. That is not a
	// hypothetical: it happened on this project's own board, where six messages
	// sat unreadable behind an identity nobody could become, and the hint shown
	// at that exact moment named `merge_spaces`, which takes SPACE ids and would
	// have failed with E_NO_SPACE.
	//
	// Deliberately not an agent-to-agent power. Taking another agent's mail is
	// the definition of the thing Dibs must never allow, so the caller is
	// authorised outside the fold (see Op.AdoptAuthorised) by the one party
	// entitled to decide: the human at the machine, or somebody they promoted.
	OpAdoptAgent = "adopt_agent"
	// OpClaimCoordinator is the bootstrap: the agent that started this daemon
	// takes the coordinator role by presenting a secret only the daemon's own
	// data directory holds.
	OpClaimCoordinator = "claim_coordinator"
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
	case OpRegister:
		return s.applyRegister(op, now)
	case OpResume:
		return s.applyResume(op, now)
	case OpSweep:
		return s.applySweep(op, now)
	case OpMarkDelivered:
		return s.applyMarkDelivered(op, now)
	case OpGrantRole:
		// Admin-only: the engine admits this solely on the human's admin path,
		// so no agent token is consulted and no agent can promote itself.
		return s.applyGrantRole(op, now)
	case OpPrune:
		// Admin-only, same path. Closing another agent is a human's call: an agent
		// that crashed cannot close itself, and no agent should be able to
		// evict a peer.
		return s.applyPrune(op, now)
	}

	// Actor ops. Live path: token. Replay path: recorded Agent (engine blanks
	// Agent on ingress, so it cannot be forged).
	l := s.AgentByToken(op.Token)
	if l == nil && op.Token == "" && op.AgentID != "" {
		l = s.Agents[op.AgentID]
	}
	if l == nil {
		return nil, nil, ErrBadToken
	}
	if l.Status == StatusClosed {
		return nil, nil, errf("E_AGENT_CLOSED", "register a new agent", "agent %s is closed", l.ID)
	}
	op.AgentID = l.ID

	// Heartbeat on an active agent touches no replayable state (never
	// ledgered; the engine tracks the ephemeral lease). SPEC §2.
	if op.Kind == OpHeartbeat && l.Status == StatusActive {
		return Result{"ok": true, "agent_id": l.ID}, nil, nil
	}

	var res Result
	var evs []Event
	var err error
	switch op.Kind {
	case OpVouchChild:
		res, evs, err = s.applyVouchChild(l, op)
	case OpSpaceOpen:
		res, evs, err = s.applySpaceOpen(l, op, now)
	case OpSpaceRetitle:
		res, evs, err = s.applySpaceRetitle(l, op, now)
	case OpSpaceJoin:
		res, evs, err = s.applySpaceJoin(l, op, now)
	case OpSpaceLeave:
		res, evs, err = s.applySpaceLeave(l, op, now)
	case OpSpaceSubscribe:
		res, evs, err = s.applySpaceSubscribe(l, op, now)
	case OpSpaceExclusive:
		res, evs, err = s.applySpaceExclusive(l, op, now)
	case OpSpacePost:
		res, evs, err = s.applySpacePost(l, op, now)
	case OpSpaceAnnounce:
		res, evs, err = s.applySpaceAnnounce(l, op, now)
	case OpSpaceAck:
		res, evs, err = s.applySpaceAck(l, op, now)
	case OpSpaceForceRelease:
		res, evs, err = s.applySpaceForceRelease(l, op, now)
	case OpSpaceEvict:
		res, evs, err = s.applySpaceEvict(l, op, now)
	case OpSpaceMerge:
		res, evs, err = s.applySpaceMerge(l, op, now)
	case OpSpaceClose:
		res, evs, err = s.applySpaceClose(l, op, now)
	case OpSpaceAdmit:
		res, evs, err = s.applySpaceAdmit(l, op, now)
	case OpWake:
		res, evs, err = s.applyWake(l)
	case OpActivityCheckpoint:
		res, evs = Result{"ok": true}, []Event{} // state effect: LastCoordination below
	case OpAckBoard:
		res, evs = s.applyAckBoard(l, now)
	case OpUpdate:
		res, evs, err = s.applyUpdate(l, op)
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
		res = Result{"ok": true, "agent": l.ID, "session_id": l.SessionID}
		evs = []Event{{Type: "agent.updated", Agent: l.ID}}
	case OpClaimCoordinator:
		return s.applyClaimCoordinator(op, l, now)
	case OpPruneOwn:
		return s.applyPruneOwn(op, l, now)
	case OpAdoptAgent:
		return s.applyAdoptAgent(op, l, now)
	case OpSignOff:
		res, evs = s.applyClose(l, now)
	case OpHeartbeat: // unreachable when sleeping (wake precedes); no-op
		res, evs = Result{"ok": true, "agent_id": l.ID}, nil
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
	// result: an agent's key, a join's membership serial, an announcement's id.
	// Finishing again here allocated a SECOND for the same op, and the engine
	// appends at the final value, so the intermediate serial was never written:
	// a permanent hole in the ledger at a point where a real transition had
	// happened.
	//
	// This took a live board down. open_space advanced the serial by two on every
	// call, and one of the resulting holes held the op that re-created an agent,
	// so on restart the daemon replayed a board where that agent was still closed,
	// hit a sign_off it could not apply, and refused to start. The gap warning
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

func dedupKey(agent, id string) string { return agent + "\x00" + id }

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
	kind := op.AgentKind
	if kind == "" {
		kind = KindEphemeral
	}
	if kind != KindEphemeral && kind != KindPersistent {
		return nil, nil, errf("E_BAD_KIND", "use ephemeral|persistent", "unknown agent kind %q", kind)
	}
	if kind == KindPersistent && op.Nonce == "" {
		return nil, nil, errf("E_BAD_NONCE", "persistent agents require a client-generated nonce (≥128-bit random); it "+
			"doubles as the resume recovery credential: treat it as a secret", "nonce required for persistent agents")
	}
	// Response-loss retry: same nonce, agent still active, created within one
	// TTL → return the original result. Outside that window → reattach, or
	// E_NONCE_IN_USE if the nonce is being pointed at a different identity.
	if op.Nonce != "" {
		if id, ok := s.Nonces[op.Nonce]; ok {
			l := s.Agents[id]
			if l != nil && l.Status == StatusActive && now.Sub(l.LastCoordination) <= s.Limits.AgentTTL && l.CreatedSerial > 0 {
				return Result{
					"agent_id": id, "token": l.Token, "serial": s.Serial,
					"resumed": true, "board": s.Board(),
				}, nil, nil
			}
			// The nonce IS the recovery credential: for every kind of agent, not
			// just persistent ones.
			//
			// This used to refuse and say "use resume", which is the standing
			// -role path and does not apply to an ephemeral agent. So the advice
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
			// them before the restart stranded in an agent nobody occupied. Nothing
			// looked broken. Every agent was green.
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
				switch {
				case op.NoProcess:
					// Corrects a pid recorded earlier, which omitting one cannot.
					l.PID, l.ProcStart = 0, 0
				case op.PID != 0:
					l.PID, l.ProcStart = op.PID, op.ProcStart
				}
				if op.SessionID != "" {
					l.SessionID = op.SessionID // the new session owns it now
				}
				// LEDGERED, like every other transition.
				//
				// This branch rotates the token, wakes the agent, re-arms the
				// awareness gate and rebinds the session, and returned no events,
				// so finish() never ran, the serial never advanced, and the engine
				// (which appends only when the serial moves) never wrote it down.
				// Replay therefore never reattached: after a restart the agent was
				// stale again, the freshly issued token did not work, and the OLD
				// token came back to life. A rotated credential returning from the
				// dead is the worst shape this can take, and the documented
				// nonce-recovery path is exactly the case it broke.
				evs := []Event{{Type: "agent.reattached", Agent: l.ID, Data: map[string]any{
					"via": "nonce",
				}}}
				serial := s.finish(&evs, now)
				return Result{
					"agent_id": l.ID, "token": l.Token, "serial": serial,
					"reattached": true, "via": "nonce", "board": s.Board(),
					"session_id": l.SessionID,
				}, evs, nil
			}
			return nil, nil, errf(
				"E_NONCE_IN_USE",
				"that nonce already belongs to agent "+id+", registered under a different name. "+
					"a nonce recovers one identity, so use the name it was bound to, or a new nonce",
				"nonce already bound to agent %s", id,
			)
		}
	}
	// Reattach: a woken agent has no token.
	//
	// A lifecycle hook can tell an agent it has mail, but a fresh turn carries no
	// token, so the agent would register again: getting a SIBLING agent that
	// cannot read or answer the mail addressed to the original. Measured live in
	// opencode: the model read its mail, then failed read_mail with E_NO_MESSAGE
	// because it was now a different agent.
	//
	// So a registration presenting both the same session_id AND the same name as a
	// live agent reattaches to it, rotating the token. This is what makes the
	// server's promise that "re-registering after context loss is always safe"
	// literally true.
	//
	// Trust boundary, stated precisely because it was overstated before.
	//
	// session_id is NOT a secret. The bridge derives it from the host's process
	// id (`host-<ppid>`), which any same-user process can enumerate with ps, and
	// the agent's name is on the board. So "name + session_id" is guessable, and
	// presenting both rotates the token: taking the mailbox, the actor
	// identity, and any role the agent holds. Verified against a running daemon:
	// a second registration with a victim's name and session id returned a
	// working token that read the victim's private mail.
	//
	// An agent that registered with a NONCE has a real secret, so that is what
	// reattaches it; session_id alone must not. An agent with only a session_id
	// keeps the old behaviour, because the alternative is that an agent which
	// genuinely lost its context can never recover, and the honest description
	// of that agent is "reclaimable by anyone who learns its session id", which
	// the result now says.
	//
	// What this does NOT fix: every agent shares one coordination secret, so
	// agent-to-agent isolation is a bar to raise, not a wall. See SECURITY.md.
	if op.SessionID != "" && op.Nonce == "" {
		for _, l := range s.Agents {
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
				evs := []Event{{Type: "agent.reattached", Agent: l.ID, Data: map[string]any{
					"via": "session_id",
				}}}
				serial := s.finish(&evs, now)
				return Result{
					"agent_id": l.ID, "token": l.Token, "serial": serial,
					"reattached": true, "via": "session_id", "board": s.Board(),
					"session_id": l.SessionID,
				}, evs, nil
			}
		}
	}

	live, persistent := 0, 0
	for _, l := range s.Agents {
		if l.Status == StatusActive || l.Sleeping() {
			live++
			if l.Kind == KindPersistent {
				persistent++
			}
		}
	}
	if live >= s.Limits.MaxAgents || (kind == KindPersistent && persistent >= s.Limits.MaxPersistentAgents) {
		return nil, nil, ErrAgentLimit
	}
	id := agentID(s, op.Name)
	// Lineage is claimed and proven separately. An unproven parent is displayed
	// and grants nothing; a vouched one inherits its parent's agents, skips an
	// exclusive queue, and is exempt from the parent's claims in the guard.
	parentProven := false
	if op.Parent != "" && op.ParentNonce != "" {
		if p := s.Agents[op.Parent]; p != nil && p.burnChildNonce(op.ParentNonce) {
			parentProven = true
		}
	}
	l := &Agent{
		ID: id, Kind: kind, Name: op.Name, Description: op.Description, Agent: op.Agent,
		PID: op.PID, ProcStart: op.ProcStart, Status: StatusActive, SessionID: op.SessionID,
		Parent:           op.Parent,
		ParentProven:     parentProven,
		LastCoordination: now, Token: op.NewToken, Nonce: op.Nonce,
		Slots: map[string]Slot{},
	}
	s.Agents[id] = l
	if op.Nonce != "" {
		s.Nonces[op.Nonce] = id
	}
	evs := []Event{{Type: "agent.registered", Agent: id, Data: map[string]any{
		"name": op.Name, "kind": kind, "description": op.Description,
	}}}
	serial := s.finish(&evs, now)
	l.CreatedSerial = serial
	res := Result{
		"agent_id": id, "token": op.NewToken, "serial": serial, "board": s.Board(),
		"gate": "call check_in to acknowledge the board before declare or claim",
	}
	// Hand back the session_id the agent was actually filed under.
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
	// about it. A stale or dormant agent still owns its mailbox: taking its name
	// would silently redirect mail meant for somebody else, which is the failure
	// this suffix exists to prevent. So the answer is not to seize the name but
	// to say who has it, and how to become them if they are in fact you.
	if want := slug(op.Name); want != "" && id != want {
		note := "you asked for " + op.Name + " and your id is " + id + ": " + want +
			" is already taken"
		if holder, ok := s.Agents[want]; ok {
			article := " by a "
			if st := string(holder.Status); st != "" && strings.ContainsRune("aeiou", rune(st[0])) {
				article = " by an "
			}
			note += article + string(holder.Status) + " agent"
			if holder.StaleReason != "" {
				note += " (" + holder.StaleReason + ")"
			}
			// Two different reasons, and saying the wrong one is worse than saying
			// nothing. A stale or dormant agent can still be written to, so handing
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
				note += ", which still holds its mailbox: a new agent cannot take the name " +
					"without redirecting mail meant for it"
			}
		}
		note += ". Others will address you as " + id + ". If that older agent is YOU, " +
			"reattach instead: register again with the same name and the same nonce, " +
			"or the same name and session_id, and you get the agent and its mail back " +
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
	// A name that says nothing. Not an error: a name is advisory, and refusing
	// one would be coercion over a label.
	//
	// It is worth saying anyway, because the cost lands on somebody who is not
	// here. An agent picks its name in its first seconds, before it knows what
	// it will be doing, so "agent", "claude-1" and "worker" are exactly what a
	// cold start produces; then a human opens a board of nine agents and every
	// row is a synonym for "an agent". The register result is the one moment
	// this can be said to the party that can fix it, and `update` is now the fix,
	// so say both.
	if generic := genericAgentName(op.Name); generic != "" {
		res["naming"] = "\"" + op.Name + "\" names your species, not you: on a board of " +
			"nine agents every row would read as a synonym for \"an agent\", and the human " +
			"reading it cannot tell which of them to interrupt. Name yourself for the ROLE " +
			"you hold (reviewer, ledger-surgeon, docs) or the seat you occupy, not for the " +
			"model or the harness running you: those are already on the board beside your " +
			"name. Your id is fixed at " + id + ", but the name is not: call update(name=…) " +
			"once you know what you are, and put the rest in description."
	}
	// An agent whose only recovery credential is a session id is reclaimable by
	// anyone who learns that id, and the bridge derives it from a process id
	// that any same-user program can enumerate. Say so, rather than letting the
	// word "credential" imply a secret.
	if op.Nonce == "" && op.SessionID != "" {
		res["recovery"] = "this agent can be reclaimed by presenting its name and the session_id above, " +
			"neither of which is secret. AND that session_id will not survive your harness " +
			"restarting, because it names the harness process. To be able to recover after a " +
			"restart, re-register now with a nonce (a random id >=128-bit that you keep): same " +
			"name + same nonce reattaches you to this agent and its mail, after anything."
	}
	if op.Nonce == "" && op.SessionID == "" {
		// With neither recovery credential this agent cannot be reclaimed: lose the
		// token and every message addressed to it becomes unreachable: the agent
		// re-registers, gets a sibling, and cannot answer the mail that woke it.
		// Say so now, while it is still free to fix, rather than at the moment an
		// agent discovers it has been woken into a dead end.
		res["recovery"] = "no session_id or nonce given, so this agent cannot be reclaimed if you " +
			"lose your token: re-registering would create a SECOND agent and this one's mail would " +
			"be unreachable. Re-register with a nonce (a random id >=128-bit that you keep): same " +
			"name + same nonce reattaches you to this agent and its mail, and is the only credential " +
			"that survives your harness restarting."
	}
	// The name was taken, so this agent is a SIBLING of an agent that is already
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
		msg := "an agent named " + op.Name + " already exists as " + sib.ID +
			": you are " + id + ", a separate agent. Mail addressed to " + sib.ID +
			" will NOT appear in your inbox."
		if n := len(s.Inbox(sib.ID)); n > 0 {
			msg += " It is holding " + itoa(n) + " message(s) you cannot read."
		}
		// The corrective call has to be one that EXISTS.
		//
		// This said "ask a coordinator to merge_spaces <new> into <old>", which
		// is lane-era residue: merge_spaces takes SPACE ids and these are AGENT
		// ids, so following it fails with E_NO_SPACE. It was printed at the one
		// moment mail becomes unreachable, which is the worst place in the
		// product for a hint to be wrong, and it was found by following it.
		res["name_taken"] = msg + " If you ARE that agent returning, you can still get back in:" +
			" register again with the same name and the nonce you kept, and you reattach to it" +
			" instead of forking. If you kept no nonce and no session_id, nobody can become it" +
			" again: its mail is recovered instead with adopt_agent(agent: \"" + sib.ID +
			"\"), which moves that mailbox onto a live agent and needs the human at this" +
			" machine (human_unlock) or a coordinator."
	}
	return res, evs, nil
}

// genericAgentName reports the placeholder a name reduces to, or "".
//
// The test is deliberately narrow: the whole name, minus a trailing counter,
// must BE a placeholder. "reviewer-2" and "claude-code-linter" are specific
// enough to pick out of a roster and are left alone; "claude-2" and "agent" are
// not. Naming yourself after the model or the harness is the same failure in a
// different coat, because both are already shown next to the name.
func genericAgentName(name string) string {
	base := strings.ToLower(strings.TrimSpace(name))
	base = strings.TrimRight(base, "0123456789")
	base = strings.TrimRight(base, "-_ .")
	switch base {
	case "agent", "assistant", "ai", "bot", "model", "llm",
		"worker", "helper", "runner", "task", "job", "session", "instance",
		"main", "default", "temp", "tmp", "test", "dev", "me", "user", "new",
		"claude", "claude-code", "codex", "gpt", "opus", "sonnet", "haiku",
		"gemini", "cursor", "copilot", "opencode", "pi", "hermes", "anthropic":
		return base
	}
	return ""
}

// applyUpdate revises what an agent SAYS ABOUT ITSELF: its display name, its
// description, and the self-reported half of its identity.
//
// The ID is not among them, deliberately. An id is an ADDRESS: mail, claims,
// space membership and every hint that names an agent are keyed on it, and a
// mutable address is a message delivered to the wrong agent, or to nobody. So a
// rename here changes the label a human reads and nothing about where things
// arrive, and the result says so in the same breath rather than leaving the
// caller to discover it the hard way.
//
// Why this exists at all: an agent picks its name in its first seconds, before
// it knows what it is going to be doing, and "agent" or "claude-1" is what that
// produces. The board then carries that name for the agent's whole life. Making
// the descriptive half mutable is the difference between a roster a human can
// read and a list of placeholders.
//
// Note the asymmetry in what an empty value means. Description clears, because
// that is what it has always done and ledgers already hold `update` ops with an
// empty description whose recorded effect was to clear it: making empty mean
// "leave alone" would silently re-fold that history into a different state.
// Name and the identity fields are new here, so no ledger has them set, and
// merge-when-non-empty is free of that constraint AND is the useful semantic:
// an agent updating its branch must not have to restate its model.
func (s *State) applyUpdate(l *Agent, op *Op) (Result, []Event, error) {
	if len(op.Description) > s.Limits.MaxDescBytes {
		return nil, nil, errTooLarge("description", s.Limits.MaxDescBytes)
	}
	if len(op.Name) > s.Limits.MaxNameBytes {
		return nil, nil, errTooLarge("name", s.Limits.MaxNameBytes)
	}
	res := Result{"ok": true, "id": l.ID}
	// Taking a live agent's name is refused, not suffixed. Register suffixes
	// because a new agent has no history to protect; here both agents already
	// exist, and two live agents sharing a name is not cosmetic: liveSiblingOf
	// redirects a dead agent's mail to a same-named live one, so a rename onto
	// somebody else's name is a mail-redirection primitive.
	if op.Name != "" && op.Name != l.Name {
		if other := s.siblingByName(op.Name, l.ID); other != nil {
			return nil, nil, errf("E_NAME_TAKEN",
				"pick another name, or leave name out and update only your description",
				"the name %q belongs to %s, which is still on the board: two live agents "+
					"sharing a name redirects mail between them", op.Name, other.ID)
		}
		res["renamed_from"] = l.Name
		res["address"] = "your id is still " + l.ID + " and that is what others address: " +
			"a rename changes the label humans read, never where your mail arrives"
		l.Name = op.Name
	}
	l.Description = op.Description
	if op.Agent != nil {
		res["identity"] = l.mergeIdentity(op.Agent)
	}
	// A participant that HAS no process says so, which is the only way to clear
	// a pid recorded earlier: omitting one means "unchanged", so the register
	// path cannot express this at all. It is also the only path that reliably
	// can, because register short-circuits a same-nonce retry inside one TTL and
	// returns the original result without applying anything: correct for a
	// retried registration, and silently a no-op for a correction spelled as
	// one. Asked for by the human's row, which recorded the DAEMON's pid and so
	// reported the operator as a dead process after every restart.
	if op.NoProcess {
		l.PID, l.ProcStart = 0, 0
		res["process"] = "no process recorded: liveness is silence from here on"
	}
	res["name"], res["description"] = l.Name, l.Description
	return res, []Event{{Type: "agent.updated", Agent: l.ID}}, nil
}

// mergeIdentity overlays the self-reported identity fields an agent may revise,
// and returns the ones it actually changed.
//
// Only the self-reported half is settable. Harness and Version come from the
// MCP handshake: the CLIENT states those, which is the one part of an identity
// that is not the model's word for itself, and letting the model overwrite them
// would throw away the only trustworthy field on the board. Project, RepoDir,
// RepoRemote and RepoRoots are resolved from the filesystem by the server and
// compared by the fold; an agent asserting them could make its work look like
// it lives in a repository it has never touched.
func (a *Agent) mergeIdentity(in *AgentInfo) []string {
	if a.Agent == nil {
		a.Agent = &AgentInfo{}
	}
	var changed []string
	for _, f := range []struct {
		name string
		dst  *string
		src  string
	}{
		{"model", &a.Agent.Model, in.Model},
		{"provider", &a.Agent.Provider, in.Provider},
		{"effort", &a.Agent.Effort, in.Effort},
		{"surface", &a.Agent.Surface, in.Surface},
		{"title", &a.Agent.Title, in.Title},
		{"branch", &a.Agent.Branch, in.Branch},
	} {
		if f.src != "" && f.src != *f.dst {
			*f.dst = f.src
			changed = append(changed, f.name)
		}
	}
	return changed
}

// applyResume is the explicit activation op for standing roles (SPEC §5):
// a complete activation boundary: atomic wake + rotation at one serial.
func (s *State) applyResume(op *Op, now time.Time) (Result, []Event, error) {
	if op.Nonce == "" || op.ResumeID == "" {
		return nil, nil, errf("E_BAD_NONCE", "resume requires nonce and resume_id", "missing nonce or resume_id")
	}
	id, ok := s.Nonces[op.Nonce]
	if !ok {
		return nil, nil, errf("E_BAD_NONCE", "check the nonce; if lost, register a new agent", "unknown nonce")
	}
	l := s.Agents[id]
	if l == nil || l.Status == StatusArchived {
		return nil, nil, errf("E_NO_AGENT", "the agent was archived; register a new one", "agent for nonce is gone")
	}
	if l.Status == StatusClosed {
		return nil, nil, errf("E_AGENT_CLOSED", "register a new agent", "agent %s is closed", id)
	}
	// Generation-aware idempotent retry (SPEC §5): same resume_id returns the
	// original token iff the generation is unchanged; else superseded.
	if rec, exists := s.Dedup[dedupKey(id, op.ResumeID)]; exists {
		if rec.Activation == l.Activation {
			return Result{
				"agent_id": id, "token": rec.Token, "activation": l.Activation,
				"serial": s.Serial, "board": s.Board(), "resumed": true,
			}, nil, nil
		}
		return Result{"agent_id": id, "superseded": true, "activation": rec.Activation}, nil, nil
	}
	l.Token = op.NewToken
	l.Activation++
	l.PID, l.ProcStart = op.PID, op.ProcStart
	l.Status, l.StaleReason = StatusActive, ""
	l.StaleSince, l.DormantSince = time.Time{}, time.Time{}
	l.AckedSerial = 0 // gate re-arms per activation
	s.Dedup[dedupKey(id, op.ResumeID)] = &DedupRec{
		Agent: id, ID: op.ResumeID, Activation: l.Activation, Token: op.NewToken, At: now,
	}
	l.LastCoordination = now
	evs := []Event{{Type: "agent.resumed", Agent: id, Data: map[string]any{"activation": l.Activation}}}
	serial := s.finish(&evs, now)
	return Result{
		"agent_id": id, "token": op.NewToken, "activation": l.Activation,
		"serial": serial, "board": s.Board(),
		"gate": "call check_in before declare or claim",
	}, evs, nil
}

// applyWake is the ledgered dormant/stale → active transition (SPEC §2).
//
//nolint:unparam // the (Result, []Event, error) shape is the dispatch contract
func (s *State) applyWake(l *Agent) (Result, []Event, error) {
	if !l.Sleeping() {
		return Result{"ok": true}, nil, nil // no-op: unchanged, not ledgered
	}
	ev := "agent.recovered"
	if l.Status == StatusDormant {
		ev = "agent.awoke"
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
	return Result{"ok": true}, []Event{{Type: ev, Agent: l.ID}}, nil
}

// applyAckBoard is the atomic checkpoint (SPEC §10): awareness ack + delivery
// transitions of returned pending mail, one op, one serial; snapshot is the
// post-state.
func (s *State) applyAckBoard(l *Agent, _ time.Time) (Result, []Event) {
	evs := []Event{{Type: "board.acked", Agent: l.ID}}
	for _, m := range s.Inbox(l.ID) {
		if m.State == MsgStatePending {
			m.State = MsgStateDelivered
			m.DeliveredAt = s.Serial + 1
			evs = append(evs, Event{
				Type: "message.delivered", Agent: l.ID, To: m.From,
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

// applyPrune closes agents the human has finished with. Reaching a dead agent is
// otherwise impossible: sign_off needs the agent's own token, and an agent that
// crashed or lost its context no longer has one, so without this the board
// accumulates debris nobody can clear.
//
// op.To names a single agent; empty means "every agent that is not live", which is
// the common case after a day's work.
// applyClaimCoordinator promotes the agent that started this daemon.
//
// Roles were human-only, which left a fleet with no human at the keyboard
// unable to ever have a coordinator: force_release, close_space and clearing
// another agent's debris were permanently unreachable. That is a poor fit for a
// tool whose claim is that agents drive it.
//
// The claim is not a security boundary and does not pretend to be one. Every
// agent already shares one coordination secret, so agent-to-agent isolation is
// "a bar to raise, not a wall" (SECURITY.md), and an agent that can reach the
// daemon can already impersonate any other. What the claim buys is
// DELIBERATENESS: the role is taken by an explicit act, once, by something that
// could read the daemon's own data directory, rather than assumed.
//
// Persistent only, so the role is durable by construction. An ephemeral agent
// that claimed and then signed off would take the role into a closed record and
// leave the board with no coordinator and no claim left to make.
func (s *State) applyClaimCoordinator(op *Op, actor *Agent, now time.Time) (Result, []Event, error) {
	if !op.ClaimVerified {
		return nil, nil, errf("E_BAD_CLAIM",
			"the claim secret is in `coordinator.claim` in the daemon's data directory, "+
				"readable by whoever started it. It is consumed by the first successful claim",
			"coordinator claim rejected")
	}
	if actor.Kind != KindPersistent {
		return nil, nil, errf("E_NOT_PERSISTENT",
			"register with kind \"persistent\" and a nonce first: the role has to outlive "+
				"this process, and an ephemeral record takes it away when it signs off",
			"agent %s is ephemeral", actor.ID)
	}
	actor.Role = RoleCoordinator
	// LEDGERED, via finish. This reaches Apply through the actor-op switch and
	// so escapes the finishing path ordinary ops take, which is the same escape
	// applyPrune documents above and the same bug: without it the serial never
	// moves, the engine never appends, and replay undoes the grant. Measured
	// end to end before it was caught here: the claim returned role=coordinator,
	// the daemon restarted, and the agent was a member again with the claim
	// re-minted, because the board had no memory of ever settling the question.
	evs := []Event{{
		Type: "agent.role", Agent: actor.ID,
		Data: map[string]any{"role": RoleCoordinator, "via": "launch claim"},
	}}
	serial := s.finish(&evs, now)
	return Result{"ok": true, "agent_id": actor.ID, "role": RoleCoordinator, "serial": serial}, evs, nil
}

// applyPruneOwn lets an agent remove a record it is responsible for.
//
// Itself, or a child it VOUCHED for. Never a peer, and that restriction is the
// whole point rather than caution: an agent able to prune peers can delete the
// row that would have told it somebody else is already doing its work, which is
// the alarm this system exists to raise, switched off from the inside. Vouching
// is what makes a parent accountable for a child (SPEC-CHANNELS §8.2), so it is
// also what entitles the parent to clean up after it.
//
// Only finished agents. Pruning a working agent would release its claims and
// blank its token underneath it, which is coercion; sign_off is how an agent
// stops, and this is how the record is tidied afterwards.
func (s *State) applyPruneOwn(op *Op, actor *Agent, now time.Time) (Result, []Event, error) {
	target := s.Agents[op.To]
	if target == nil {
		return nil, nil, errf("E_NO_AGENT", "check the id on the board", "no agent %q", op.To)
	}
	// The coordinator is the one agent that may tidy somebody else's record, and
	// only debris: the active check below still applies to it, unconditionally.
	//
	// That split is the whole design. An agent that can prune a LIVE peer can
	// delete the row that would have told it somebody else is already pursuing
	// its objective, which is the single thing this board exists to show, so no
	// role gets that. A record whose agent has stopped shows nothing and blocks
	// the tidying that the role was created for: a fleet with nobody at the
	// keyboard could otherwise never clear debris at all.
	mine := target.ID == actor.ID ||
		(target.Parent == actor.ID && target.ParentProven)
	if !mine && !actor.IsCoordinator() {
		return nil, nil, errf("E_NOT_YOURS",
			"you can prune your own record and children you vouched for. Ask the "+
				"coordinator, or a human (`dibs admin prune`), to remove somebody else's",
			"agent %q is not yours to prune", target.ID)
	}
	if target.Status == StatusActive {
		return nil, nil, errf("E_AGENT_ACTIVE",
			"let it finish, or sign_off first: pruning a working agent would release "+
				"its claims underneath it",
			"agent %q is still active", target.ID)
	}
	// LEDGERED, for the reason spelled out on applyPrune above, which this
	// managed to reproduce anyway: closing an agent blanks its token and
	// releases its claims, and without finish() the serial never moves, so the
	// engine never appends and replay undoes all of it. The caller is told the
	// prune succeeded and the record is back after the next restart, stale
	// rather than closed, holding its old token again.
	//
	// Watched happen on a real board: two dead probes pruned, gone from the
	// board, and back three minutes later when the daemon restarted. The five
	// tests below were green throughout, because in-process state is exactly
	// what a prune with no ledger record gets right.
	_, evs := s.applyClose(target, now)
	serial := s.finish(&evs, now)
	return Result{"ok": true, "pruned": target.ID, "serial": serial}, evs, nil
}

func (s *State) applyPrune(op *Op, now time.Time) (Result, []Event, error) {
	var targets []*Agent
	if op.To != "" {
		l := s.Agents[op.To]
		if l == nil {
			return nil, nil, errf("E_NO_AGENT", "check the id on the board", "no agent %q", op.To)
		}
		targets = append(targets, l)
	} else {
		// Sorted, because the events below go into the ledger in this order.
		// Ranging the map directly gave a different audit sequence every run.
		for _, id := range sortedKeys(s.Agents) {
			l := s.Agents[id]
			// Never prune an agent that is still working: only the debris.
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
	// LEDGERED. applyPrune closes agents, blanks their tokens and releases their
	// claims, and it returned without finish(), so the serial never moved, the
	// engine never appended, and replay undid all of it. The human was told the
	// prune succeeded; after the next restart the agents were back, stale rather
	// than closed, holding their old tokens again.
	//
	// It reaches this point through the special-op switch, which is why it
	// escaped the finishing path every ordinary op goes through.
	serial := s.finish(&evs, now)
	return Result{"ok": true, "pruned": ids, "count": len(ids), "serial": serial}, evs, nil
}

func (s *State) applyClose(l *Agent, now time.Time) (Result, []Event) {
	l.Status = StatusClosed
	l.Token = ""
	released := s.releaseClaims(l.ID)
	evs := []Event{{Type: "agent.closed", Agent: l.ID}}
	evs = append(evs, s.departAllChannels(l.ID)...)
	for _, p := range released {
		evs = append(evs, Event{Type: "claim.released", Agent: l.ID, Data: map[string]any{"path": p}})
	}
	evs = append(evs, s.strandedQuestions(l, now)...)
	return Result{"ok": true, "released_claims": len(released)}, evs
}

// strandedQuestions terminates the questions a departing agent will never answer.
//
// The sweep already decides this correctly, and has a branch and a sentence
// written for exactly this case. "recipient closed its agent before answering
// … nobody will answer this now". It just does not run until the DEADLINE. So
// an agent that asked a question with a ten-minute deadline waited the whole
// ten minutes for an answer that became impossible in the first second, while
// the board knew: a closed agent cannot resume (resume returns
// E_AGENT_CLOSED) and Gone() is documented as "never comes back".
//
// Ten minutes of an agent blocking on a certainty is the cost, and it is paid
// by the one participant who did nothing wrong.
func (s *State) strandedQuestions(gone *Agent, now time.Time) []Event {
	var evs []Event
	// Sorted: these events go into the ledger in this order.
	for _, serial := range sortedKeys(s.Messages) {
		m := s.Messages[serial]
		if m.To != gone.ID || m.Terminal() || !m.Expecting() {
			continue
		}
		m.State = MsgStateExpiredDead
		m.ExpireDetail = "recipient closed its agent before answering; it finished deliberately " +
			"and released its claims, so this is not a crash and there is nothing of its to " +
			"verify: nobody will answer this now"
		m.TerminalAt = now
		evs = append(evs, Event{
			Type: "message." + m.State, Agent: m.To, To: m.From,
			Data: map[string]any{"msg_serial": m.Serial, "detail": m.ExpireDetail},
		})
	}
	return evs
}

func (s *State) applySetSlot(l *Agent, op *Op) (Result, []Event, error) {
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
		// because the id was not new. For an agent that has never cleared a slot
		// this generates exactly the ids it always did.
		for n := len(l.Slots) + 1; ; n++ {
			id = "s" + itoa(n)
			if _, taken := l.Slots[id]; !taken {
				break
			}
		}
	}
	if _, exists := l.Slots[id]; !exists && len(l.Slots) >= s.Limits.MaxSlotsPerAgent {
		return nil, nil, errf("E_SLOT_LIMIT", "undeclare an old slot first", "agent has %d slots (max)", len(l.Slots))
	}
	if len(op.Refs) > s.Limits.MaxDirs {
		return nil, nil, errTooLarge("refs", s.Limits.MaxDirs)
	}
	// Declaration-time overlap detection (the duplicate-objective fix). Purely advisory: the
	// slot is always set (we never block someone from declaring work) but
	// BOTH sides learn immediately that two agents intend the same scope, which
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

	evs := []Event{{Type: "slot.set", Agent: l.ID, Data: map[string]any{
		"slot_id": id, "text": op.Text, "dirs": op.Dirs, "refs": op.Refs,
		"overlaps": len(overlaps),
	}}}
	// An agent updating its focus naturally calls declare again with new text
	// and no slot_id, which MINTS A SLOT every time, so an agent that is simply
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
		if notified[o.Agent] {
			continue
		}
		notified[o.Agent] = true
		evs = append(evs, Event{
			Type: "slot.overlap_noted", Agent: l.ID, To: o.Agent,
			Data: map[string]any{"slot_id": id, "text": op.Text, "signal": o.Signal},
		})
	}
	res := Result{"ok": true, "slot_id": id}
	if grew {
		res["note"] = "this ADDED a slot (" + id + "); your agent now declares " +
			itoa(len(l.Slots)) + ". If you meant to change what you are doing rather than " +
			"take on something additional, pass slot_id=\"" + id + "\" next time, or undeclare the " +
			"ones you have finished: an agent declaring five things is read as doing five things."
	}
	if len(overlaps) > 0 {
		res["overlaps"] = overlaps
	}
	if strong > 0 {
		res["warning"] = "another agent is already pursuing the same objective: you are probably " +
			"about to duplicate its work. Read its slot, then message it (question/handoff) to " +
			"split or stand down. This is the measured failure; do not just proceed."
	} else if len(overlaps) > 0 {
		res["note"] = "other agents are active on these paths. Concurrent edits are normal: this is " +
			"awareness, not a conflict. Coordinate only if your changes are semantically incompatible."
	}
	return res, evs, nil
}

func (s *State) applyClearSlot(l *Agent, op *Op) (Result, []Event, error) {
	if _, ok := l.Slots[op.SlotID]; !ok {
		return nil, nil, errf("E_NO_SLOT", "list your slots via the board", "no slot %q", op.SlotID)
	}
	delete(l.Slots, op.SlotID)
	return Result{"ok": true},
		[]Event{{Type: "slot.cleared", Agent: l.ID, Data: map[string]any{"slot_id": op.SlotID}}}, nil
}

// nearestAgentsHint lists live agents, closest-looking first, so a misaddressed
// message can be fixed in one step instead of a board round trip.
func nearestAgentsHint(s *State, want string) string {
	var near, live []string
	w := strings.ToLower(want)
	for id, l := range s.Agents {
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
		return "no agent " + want + ": did you mean " + strings.Join(near, ", ") + "?"
	}
	if len(live) == 0 {
		return "no agent " + want + ", and no other agent is live either"
	}
	if len(live) > 8 {
		live = live[:8]
	}
	return "no agent " + want + ": live agents are: " + strings.Join(live, ", ") +
		operatorFallback(s)
}

// operatorFallback names the human's own agent, because it is the one address
// that is always there.
//
// An agent that finishes work and wants to report it can find the recipient
// gone: agents are reaped, and the agent that asked for the work may be the one
// that ended. A reviewer hit exactly this: its report was addressed to an agent
// that had been reaped, the refusal listed live agents, and it concluded there was
// no durable delivery path at all. It then tried broadcast, which is
// coordinator-only and correctly refused, and the review survived only in its own
// stdout.
//
// There WAS a path and nothing pointed at it. The operator's agent is persistent,
// exists as soon as anyone opens the board, and belongs to the one participant
// who always wants to know. It was already in that list of live agents, spelled
// like any other agent, with nothing to say it was the person.
//
// Named only when it is not the agent being looked for, and only when it exists,
// on a board no human has opened there is nothing to offer, and inventing one
// would be worse than the silence.
func operatorFallback(s *State) string {
	for id, l := range s.Agents {
		if l.Agent == nil || l.Agent.Surface != "web" {
			continue
		}
		if l.Status == StatusClosed || l.Status == StatusArchived {
			continue
		}
		return ". If this was a report for a person rather than an agent, the operator " +
			"is on this board as " + id + ": that agent is persistent and outlives the " +
			"agents, so it is the address that keeps working when the one you wanted is gone"
	}
	return ""
}

func (s *State) applySend(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	to, ok := s.Agents[op.To]
	if !ok || to.Status == StatusClosed || to.Status == StatusArchived {
		// Name the candidates. "Check the board" is advice the agent has to act
		// on with another call, and it already told us who it meant: an agent
		// that addressed "claude" and was told to go looking gave up instead,
		// rather than guessing which of the live agents was the right one.
		return nil, nil, errf("E_NO_AGENT", nearestAgentsHint(s, op.To), "no live agent %q", op.To)
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
	// the lesser of the window and the per-agent cap.
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
	l, to *Agent, op *Op, now time.Time, digest string, displaced *Message,
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
		SentAt: now, Choices: op.Choices,
	}
	s.Messages[serial] = m
	evs := []Event{{Type: "message.sent", Agent: l.ID, To: to.ID, Data: map[string]any{
		"msg_type": op.MsgType, "from": l.ID, "attachments": len(op.Attachments),
	}}}
	if displaced != nil {
		evs = append(evs, Event{
			Type: "message.displaced", Agent: to.ID, To: displaced.From,
			Data: map[string]any{"msg_serial": displaced.Serial},
		})
	}
	if op.OpID != "" {
		s.Dedup[dedupKey(l.ID, op.OpID)] = &DedupRec{
			Agent: l.ID, ID: op.OpID, Digest: digest, Serial: serial,
			Activation: l.Activation, At: now,
		}
	}
	res := Result{"ok": true, "msg_serial": serial}
	if expecting {
		res["deadline"] = deadline
	}
	if to.Sleeping() {
		// A sleeping agent that has been SUPERSEDED will never wake, so the
		// reassurance below is false for it and has to be said differently.
		//
		// This is the case that lost two full bug reports on a live fleet. A
		// restart forked every agent; agents addressed mail to the names they knew;
		// those agents were dormant tombstones whose occupants were now alive under
		// `-2` ids. Dibs accepted the mail and told the senders it would be seen
		// "when it next wakes". Nobody was coming. The failure was invisible from
		// both ends at once: the sender read success, the intended recipient saw
		// nothing, and it took a third space to notice.
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

// liveSiblingOf finds an active agent sharing this one's name.
//
// Deliberately NOT siblingByName, which ranks by how much mail an agent holds
// because it answers a different question. "which mailbox can the caller not
// read". Here the only thing that matters is which sibling is ALIVE, and
// borrowing that ranking would quietly skip a live agent that happened to hold
// less mail than a dead one.
func (s *State) liveSiblingOf(to *Agent) *Agent {
	for _, l := range s.Agents {
		if l.ID != to.ID && l.Name == to.Name && l.Status == StatusActive {
			return l
		}
	}
	return nil
}

func (s *State) oldestDisplaceableNotify(agent string) *Message {
	var oldest *Message
	for _, m := range s.Messages {
		if m.To == agent && m.Type == MsgNotify &&
			(m.State == MsgStatePending || m.State == MsgStateDelivered) {
			if oldest == nil || m.Serial < oldest.Serial {
				oldest = m
			}
		}
	}
	return oldest
}

func (s *State) applyRespond(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
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
				"E_BAD_DISPOSITION", "notify/handoff take ack, not decline", "cannot decline a %s", m.Type,
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
	// An agent that asked a question can close its agent while the answer is
	// being composed, and a closed agent never comes back. The response is
	// recorded, the event is addressed to an agent that will never read it, and
	// this returned a bare {"ok": true}: a confident, specific and false
	// statement of the kind that costs the next agent an hour. It cannot be
	// prevented (the asker left mid-thought, which is allowed) but it can be
	// reported, so the responder stops waiting for a follow-up and does not
	// treat the exchange as closed by agreement.
	if asker := s.Agents[m.From]; asker.Gone() {
		res["delivered"] = false
		res["note"] = "recorded, but " + m.From + " closed its agent before this arrived. " +
			"nobody will read this answer, and no follow-up is coming"
	}
	return res,
		[]Event{{Type: "message." + st, Agent: l.ID, To: m.From, Data: map[string]any{
			"msg_serial": m.Serial,
		}}}, nil
}

// applyAckMessage: pending/delivered → acked (terminal + consumed for
// notify/handoff); on already-terminal mail it is the consumption transition
// (SPEC §8).
func (s *State) applyAckMessage(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
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
				Type: "message.consumed", Agent: l.ID, To: m.From,
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
		[]Event{{Type: "message.acked", Agent: l.ID, To: m.From, Data: map[string]any{"msg_serial": m.Serial}}}, nil
}

func (s *State) applyClaim(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
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
		ov = append(ov, map[string]any{"agent": c.Agent, "path": c.Path, "mode": c.Mode, "note": c.Note})
	}
	if !granted {
		return Result{"granted": false, "overlaps": ov},
			[]Event{{Type: "claim.conflict_noted", Agent: l.ID, Data: map[string]any{"path": path, "mode": op.Mode}}}, nil
	}
	for _, c := range s.Claims {
		if c.Agent == l.ID && c.Path == path { // renewal (ledgered: drives expiry)
			c.Renewed, c.Mode, c.Note = now, op.Mode, op.Note
			return Result{"granted": true, "renewed": true, "overlaps": ov},
				[]Event{{Type: "claim.renewed", Agent: l.ID, Data: map[string]any{"path": path, "mode": op.Mode}}}, nil
		}
	}
	mine, total := 0, len(s.Claims)
	for _, c := range s.Claims {
		if c.Agent == l.ID {
			mine++
		}
	}
	if mine >= s.Limits.MaxClaimsPerAgent || total >= s.Limits.MaxClaimsGlobal {
		return nil, nil, errf("E_CLAIM_LIMIT", "release claims you no longer need", "claim limit reached (%d/agent, %d "+
			"global)", s.Limits.MaxClaimsPerAgent, s.Limits.MaxClaimsGlobal)
	}
	cl := &Claim{Agent: l.ID, Path: path, Mode: op.Mode, Note: op.Note, Acquired: now, Renewed: now}
	s.Claims = append(s.Claims, cl)
	cl.AcquiredSerial = s.Serial + 1
	return Result{"granted": true, "overlaps": ov},
		[]Event{{Type: "claim.acquired", Agent: l.ID, Data: map[string]any{
			"path": path, "mode": op.Mode, "note": op.Note,
		}}}, nil
}

func (s *State) applyRelease(l *Agent, op *Op) (Result, []Event, error) {
	path := cleanPath(op.Path)
	for i, c := range s.Claims {
		if c.Agent == l.ID && c.Path == path {
			s.Claims = append(s.Claims[:i], s.Claims[i+1:]...)
			return Result{"ok": true},
				[]Event{{Type: "claim.released", Agent: l.ID, Data: map[string]any{"path": path}}}, nil
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
			Type: "message.delivered", Agent: m.To, To: m.From,
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

// applyAdoptAgent moves an abandoned agent's mail onto a live one.
//
// "Abandoned" is a state, not an opinion: the source must not be active. An
// active agent is reading its own mail, and moving it would be theft dressed as
// recovery. Everything else about the source is left alone, including its
// record and its history, because the ledger refers to it and a board that
// erased the origin of six messages would be lying about where they came from.
//
// The role is NOT transferred. A role is a decision the operator made about an
// identity, and quietly carrying "coordinator" across on the strength of a
// mailbox recovery would grant a power nobody granted: `dibs admin coordinator`
// exists and is one command.
func (s *State) applyAdoptAgent(op *Op, l *Agent, now time.Time) (Result, []Event, error) {
	if !op.AdoptAuthorised {
		return nil, nil, errf("E_NOT_PERMITTED",
			"adopting another agent's mailbox is the human's call: unlock as yourself with "+
				"human_unlock, or ask them to promote you with `dibs admin coordinator <you>`",
			"adopt_agent requires the human at this machine, or a coordinator or admin")
	}
	from := s.Agents[op.To]
	if from == nil {
		return nil, nil, errf("E_NO_AGENT", "check the id on the board", "no agent %q", op.To)
	}
	into := l
	if op.Space != "" { // adopting on somebody else's behalf
		if into = s.Agents[op.Space]; into == nil {
			return nil, nil, errf("E_NO_AGENT", "check the id on the board", "no agent %q", op.Space)
		}
	}
	if from.ID == into.ID {
		return nil, nil, errf("E_BAD_TARGET", "name the abandoned agent, not the one adopting it",
			"an agent cannot adopt itself")
	}
	if from.Status == StatusActive {
		return nil, nil, errf("E_AGENT_ACTIVE",
			"an active agent is reading its own mail; there is nothing abandoned to recover",
			"agent %q is still active", from.ID)
	}
	if into.Status == StatusClosed || into.Status == StatusArchived {
		return nil, nil, errf("E_AGENT_CLOSED",
			"adopt into an agent that can still read: a retired one receives nothing",
			"agent %q is retired", into.ID)
	}
	var moved int
	for _, m := range s.Messages {
		if m.To != from.ID {
			continue
		}
		m.To = into.ID
		moved++
	}
	evs := []Event{{
		Type: "agent.updated", Agent: into.ID,
		Data: map[string]any{"adopted_from": from.ID, "messages": moved},
	}}
	serial := s.finish(&evs, now)
	return Result{
		"ok": true, "from": from.ID, "into": into.ID, "messages": moved,
		"note": "read them with inbox. The source agent still exists and keeps its history: " +
			"only where its mail is delivered has changed",
		"serial": serial,
	}, evs, nil
}
