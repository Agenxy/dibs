package core

// The command vocabulary: the Op type and nothing that folds it.
//
// Split out of apply.go, which reached the 2000-line limit. The division is
// not arbitrary. Op is the ON-DISK FORMAT: every json tag here is frozen the
// moment a release writes one, and renaming a tag is silent data loss rather
// than a rename (TestLedgerFieldNamesAreFrozen, and the fingerprint beside it
// that a find-and-replace cannot recompute). Keeping it in its own file means
// a change to the wire format is a change to THIS file, and shows up as one in
// a diff, instead of arriving as four lines in the middle of the fold.

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

	// HumanMint marks the ONE registration that may create or reattach the
	// operator's own agent. An ingress decision like the two above, and the
	// reasoning is with the guard that reads it (engine.wouldTakeHumanIdentity).
	//
	// No json name, deliberately: it is neither ledgered nor settable by a
	// caller, so replay sees the ordinary registration it always was.
	HumanMint bool `json:"-"`

	// KeepDescription means the caller OMITTED `description`, so the engine
	// fills the current one in rather than letting the fold assign "".
	//
	// No json name, like HumanMint: an ingress decision, and the op that reaches
	// the ledger carries the resolved text, so replay is unchanged. The fold
	// assigns Description unconditionally and must keep doing so, because an op
	// already on disk that cleared a description meant to clear it.
	KeepDescription bool `json:"-"`

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
	Choices []string `json:"choices,omitempty"`
	// Grant is the role a request ASKS FOR, so that approving the request IS the
	// grant rather than a note recording that somebody agreed one should happen.
	Grant string `json:"grant,omitempty"`
	// Adopt is the ABANDONED agent a request asks to reclaim, so that approving
	// it moves that mailbox rather than telling somebody they may go and do it.
	Adopt     string `json:"adopt,omitempty"`
	ProcStart int64  `json:"proc_start,omitempty"`
	NewToken  string `json:"token,omitempty"` // engine-generated; encrypted at rest
	Nonce     string `json:"nonce,omitempty"` // encrypted at rest
	ResumeID  string `json:"resume_id,omitempty"`
	SessionID string `json:"session_id,omitempty"` // harness session, for hook lookup
	// SessionAlias is another name this same harness session goes by, joined by
	// the daemon at ingress. See Agent.SessionAliases.
	//
	// THE CALLER CAN WRITE IT, and this comment used to say the opposite.
	//
	// "Never sent by a caller" was true of every tool SCHEMA and false of the
	// wire: mcp.go fills this from `_meta.threadId`, which is transport
	// metadata the caller writes, so the value arrives from whoever is calling.
	// The old sentence was sound about tools and silent about _meta, which is
	// why nobody spotted it, and a future change that trusted it would have been
	// trusting the wrong surface. Reported by an agent that read the tree.
	//
	// What makes it safe is not the claim, it is mayClaimSession at ingress: an
	// id already held by a DIFFERENT agent is refused and cleared, unless that
	// agent only inherited it by inference. Nothing here is trusted; it is
	// vetted.
	SessionAlias string     `json:"session_alias,omitempty"`
	Agent        *AgentInfo `json:"agent,omitempty"`  // who is behind the agent (descriptive only)
	Parent       string     `json:"parent,omitempty"` // the agent that spawned this one (§8.2)
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
	// PurgeMail says this sweep may take a purged agent's mailbox with it.
	//
	// A FLAG BECAUSE THE FOLD CHANGED. Purging used to delete the agent row and
	// leave its mail, so the next agent to take that name inherited the mailbox.
	// Fixing that changed what an EXISTING op does, and replay applies today's
	// Apply to yesterday's ops: a sweep recorded by v0.0.6 was replayed with the
	// new behaviour, deleted mail the original run had kept, and a later ack
	// that v0.0.6 had accepted then failed with E_NO_MESSAGE, so the daemon
	// refused its own ledger on upgrade.
	//
	// Sweeps written by this version set it; every historical one lacks it and
	// keeps the semantics it was written under. This is the same rule AGENTS.md
	// gives for validation in Apply, applied to changed behaviour: record the
	// decision in the Op so replay makes the decision that was actually made.
	PurgeMail bool `json:"purge_mail,omitempty"`

	// RestoreNonce says this register may put a recovered agent's nonce back.
	//
	// THE SAME HAZARD AS PurgeMail, in the same file, three weeks later. The
	// recovery branch in applyRegister now restores Agent.Nonce, which is
	// right: archival blanks it while keeping the nonce INDEX, so a recovered
	// agent had no durable identity, AgentIdentity returned "", and a declared
	// role could never reconcile onto it again. An admin dormant for a month
	// came back as itself, with its mail and its claims, and permanently
	// without the role dibs.toml grants it.
	//
	// But it is a change to the FOLD, and the fold is retroactive: Apply runs
	// over ops accepted by older code. v0.0.6 did not restore the nonce, so
	// every register op already on disk means something different under the
	// two versions. The divergence is not abstract. A later same-session
	// registration finds the nonce present, skips that row and mints a
	// SIBLING, so one ledger reconstructs two different boards depending on
	// which binary reads it, and `state == fold(ledger)` stops being true
	// across an upgrade.
	//
	// Registrations written by this version set it; every historical one lacks
	// it and keeps the semantics it was written under. Same rule AGENTS.md
	// gives for validation in Apply, applied to changed behaviour: record the
	// decision in the Op so replay makes the decision that was actually made.
	RestoreNonce bool `json:"restore_nonce,omitempty"`

	// SessionGuessed says this op's SessionAlias was INFERRED by the daemon from
	// the working directory, rather than stated by the caller.
	//
	// The two are not equally good and were previously indistinguishable. A
	// caller behind the stdio bridge states the session it is running inside on
	// every call; with nothing stated, the engine falls back to picking an id
	// announced from this cwd recently and assuming the agent registering now is
	// that session. That guess is how a swept row's still-LIVE session id was
	// handed to the next agent in its directory, which then received its wake
	// notifications for hours.
	//
	// Recording which it was lets a first-hand claim take an id back from a
	// guess, without letting anything take one from an agent that stated it.
	// Set at ingress and carried into the ledger, so replay reaches the same
	// board rather than re-deciding with today's children map, which is exactly
	// the hazard PurgeMail and RestoreNonce above were added for.
	SessionGuessed bool `json:"session_guessed,omitempty"`

	// SessionTakenFrom is the agent that held this session id and is losing it,
	// resolved at ingress and recorded so replay strips the same row.
	//
	// A session id names a HARNESS THREAD, and a thread has one occupant. The
	// register path refused any id another agent held unless that agent was
	// closed or archived, so a DORMANT holder blocked the live session behind it
	// forever: the rightful session was told "already held by <agent>" and
	// pointed at register-with-your-nonce, which is a call only that other agent
	// can make. Measured on this project's own board, where every hook from a
	// working session resolved to nobody for days because one dormant row held
	// its id and nothing could take it back.
	//
	// Dormant is not "still using it". An agent that has stopped answering is
	// not the live thread presenting that id, and nothing about mail moves with
	// this: the old row keeps its mailbox and its history, and only where WAKES
	// are delivered changes. An ACTIVE holder still wins, because two live
	// agents claiming one thread is a real conflict rather than stale state.
	SessionTakenFrom string `json:"session_taken_from,omitempty"`

	// ReleaseSession drops the CALLER's own session bindings, primary and
	// aliases, so the session they belong to can claim them back.
	//
	// The repair for a binding that is already wrong. Recording whether a
	// binding was stated or guessed stops NEW ones going astray and does nothing
	// for the ones on disk: a binding written before that field existed decodes
	// as stated, deliberately, because reading old ones as guesses would make
	// every agent on an upgraded board reclaimable by whoever states its id
	// first. So the rightful session is refused its own id with E_SESSION_TAKEN
	// and there is otherwise no way out. Measured here, where one agent held
	// another's session id across a daemon restart and the owner could not take
	// it back.
	//
	// Only ever the caller's OWN, which is what makes it safe without a role: an
	// agent giving up its own bindings can strand nothing but itself, and it is
	// the one participant that can always tell whether an id is really its
	// session.
	ReleaseSession bool `json:"release_session,omitempty"`

	// V7Semantics says this op was written by a build that applies v0.0.7's
	// mailbox and prune repairs, and may therefore have them applied on replay.
	//
	// THE SAME HAZARD AS PurgeMail AND RestoreNonce, arrived at twice more. Two
	// fixes changed what an EXISTING op does: register began raising a new
	// agent's watermark past mail left by a vanished predecessor, and prune
	// stopped closing an already-closed agent or advancing the serial for a
	// no-op. Both are right for ops written from now on and both rewrite
	// history: replay of a v0.0.6 ledger reconstructs a different inbox, and
	// silently drops an `agent.closed` the original fold really did emit,
	// routing a deliberate semantic change through the serial-gap path that
	// exists for corruption.
	//
	// Recording it means old ops keep exactly the semantics they were written
	// under, which is the only version of this that lets a daemon replay its own
	// history. One flag rather than two because both repairs arrived in the same
	// version and a build either has them or does not; splitting it would freeze
	// two names for one fact. Found by the pre-release review, twice.
	V7Semantics bool     `json:"v7_semantics,omitempty"`
	DeadAgents  []string `json:"dead_agents,omitempty"`
	StaleAgents []string `json:"stale_agents,omitempty"`
	AlivePIDs   []int    `json:"alive_pids,omitempty"`

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
