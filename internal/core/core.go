// Package core is the pure deterministic heart of Dibs: a state machine with
// no I/O, no goroutines, and no wall clock. Every mutation flows through
// Apply(op, now) → events. SPEC §2 invariant: an op is ledgered iff it changed
// replayable state, every change has exactly one serial, and unledgered
// activity never mutates replayable state, so replay is exact:
// state == fold(ledger).
package core

import (
	"strings"
	"time"
)

// AgentKind distinguishes session-scoped agents from standing roles (SPEC §6).
type AgentKind string

// Agent kinds.
const (
	KindEphemeral  AgentKind = "ephemeral"
	KindPersistent AgentKind = "persistent"
)

// AgentStatus is the lifecycle state of an agent.
type AgentStatus string

// Agent lifecycle states. Unreachable is reserved for v2 federation.
const (
	StatusActive      AgentStatus = "active"
	StatusStale       AgentStatus = "stale"   // ephemeral: coordination lost
	StatusDormant     AgentStatus = "dormant" // persistent: expected sleep
	StatusClosed      AgentStatus = "closed"
	StatusArchived    AgentStatus = "archived"
	StatusUnreachable AgentStatus = "unreachable"
)

// Message types.
const (
	MsgNotify   = "notify"
	MsgQuestion = "question"
	MsgRequest  = "request"
	MsgHandoff  = "handoff"
)

// Message states (SPEC §8).
const (
	MsgStatePending        = "pending"
	MsgStateDelivered      = "delivered"
	MsgStateAcked          = "acked"
	MsgStateAnswered       = "answered"
	MsgStateApproved       = "approved"
	MsgStateDenied         = "denied"
	MsgStateDeclined       = "declined"
	MsgStateExpiredSilent  = "expired_unanswered"
	MsgStateExpiredDormant = "expired_recipient_dormant"
	MsgStateExpiredDead    = "expired_recipient_dead"
	MsgStateDisplaced      = "displaced"
)

// Agent roles. A fleet is commonly one or a few trusted coordinator agents
// directing many workers, so Dibs models that directly rather than making the
// human relay for them.
//
// Three tiers, each granted only by the human:
//
//	member       default. Its own agent, its own mail.
//	coordinator  BREADTH, not intrusion: address the whole fleet, unstick a
//	             shared resource. Still cannot read another agent's mail.
//	admin        everything a human can do, INCLUDING reading all mail.
//
// The coordinator/admin split is the load-bearing one. Most fleets want a lead
// agent that can direct workers without reading their private exchanges, and
// that is coordinator. Admin is a deliberate, human-granted escalation for an
// agent the operator trusts as they trust themselves: it is the god view, and
// it is named plainly so nobody grants it by accident.
const (
	RoleMember      = "member"      // default
	RoleCoordinator = "coordinator" // breadth: broadcast + force_release
	RoleAdmin       = "admin"       // everything a human can do, mail included
)

// Claim modes.
const (
	ClaimShared    = "shared"
	ClaimExclusive = "exclusive"
)

// Limits bounds every resource in the system (SPEC §11). All enforced in Apply.
type Limits struct {
	MaxAgents           int `json:"max_agents"`
	MaxPersistentAgents int `json:"max_persistent_agents"`
	MaxSlotsPerAgent    int `json:"max_slots_per_agent"`
	MaxClaimsPerAgent   int `json:"max_claims_per_agent"`
	MaxClaimsGlobal     int `json:"max_claims_global"`
	MaxMailboxDepth     int `json:"max_mailbox_depth"`
	TerminalRetention   int `json:"terminal_retention"` // unconsumed terminal msgs kept per agent
	// AnnouncementRetention bounds SETTLED announcements kept per space.
	//
	// Every other collection in replayed state has a bound and this one did not:
	// announcements were added on every announce and removed only when an
	// empty auto-opened space was reclaimed. A standing space a human opened
	// is never reclaimed, so its history grew for the life of the board, and it
	// is replayed into memory on every daemon start, so the cost compounds.
	//
	// Only fully acknowledged ones are eligible. An open announcement is an
	// outstanding obligation, and `unacked` is documented as staying visible
	// forever precisely because redelivery gave up on it; dropping either would
	// discard the thing the mechanism exists to guarantee.
	AnnouncementRetention int `json:"announcement_retention"`
	// PostRetention bounds remarks kept per agent. Posts oblige nobody, so
	// unlike announcements the oldest can always be dropped unexamined.
	PostRetention int `json:"post_retention"`
	MaxBodyBytes  int `json:"max_body_bytes"`
	MaxNameBytes  int `json:"max_name_bytes"`
	MaxDescBytes  int `json:"max_desc_bytes"`
	MaxNoteBytes  int `json:"max_note_bytes"`
	MaxPathBytes  int `json:"max_path_bytes"`
	MaxDirs       int `json:"max_dirs"`
	MaxIDBytes    int `json:"max_id_bytes"` // nonce / op_id / resume_id
	// Attachments & blob store (SPEC-ATTACHMENTS A9).
	MaxBlobSize       int           `json:"max_blob_size"`
	MaxAttachments    int           `json:"max_attachments"`
	BlobStoreBytes    int           `json:"blob_store_bytes"`     // global cap
	PerAgentBlobBytes int           `json:"per_agent_blob_bytes"` // per-agent quota (P1-3)
	MaxFilerefHash    int           `json:"max_fileref_hash"`
	DedupPerAgent     int           `json:"dedup_per_agent"`
	DedupWindow       time.Duration `json:"dedup_window"`
	AgentTTL          time.Duration `json:"agent_ttl"`
	// IdleTTL applies to agents that gave no PID, where silence is the only
	// signal available and a human-paced surface is silent by nature.
	IdleTTL          time.Duration `json:"idle_ttl"`
	StaleGrace       time.Duration `json:"stale_grace"`
	DormancyMax      time.Duration `json:"dormancy_max"`
	ArchiveRetention time.Duration `json:"archive_retention"`
	// ConsumedRetention keeps consumed terminal messages readable (the
	// sender must have a real window to fetch the outcome via read_mail
	// before GC: found by real-agent testing).
	ConsumedRetention  time.Duration `json:"consumed_retention"`
	ClaimLease         time.Duration `json:"claim_lease"`
	ClaimMaxLife       time.Duration `json:"claim_max_life"`
	DefaultDeadline    time.Duration `json:"default_deadline"`
	MaxDeadline        time.Duration `json:"max_deadline"`
	MaxDeadlineDormant time.Duration `json:"max_deadline_persistent"`
	// Blob eviction windows (SPEC-ATTACHMENTS A9). Grace protects a
	// freshly-put, not-yet-attached (refcount 0) blob from immediate
	// eviction, bounding the put→send race; TTL is the hard unreferenced life.
	BlobGraceWindow time.Duration `json:"blob_grace_window"`
	BlobTTL         time.Duration `json:"blob_ttl"`
}

// DefaultLimits are the SPEC §11 defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxAgents:           64,
		MaxPersistentAgents: 16,
		MaxSlotsPerAgent:    32,
		MaxClaimsPerAgent:   32,
		MaxClaimsGlobal:     256,
		MaxMailboxDepth:     256,
		TerminalRetention:   128,
		// Comfortably above read_space's default page of 50, so garbage collection
		// never truncates history a reader could otherwise still page through.
		AnnouncementRetention: 128,
		PostRetention:         128,
		MaxBodyBytes:          32 * 1024,
		MaxNameBytes:          128,
		MaxDescBytes:          1024,
		MaxNoteBytes:          512,
		MaxPathBytes:          1024,
		MaxDirs:               16,
		MaxIDBytes:            128,
		MaxBlobSize:           64 * 1024 * 1024, // 64 MiB
		MaxAttachments:        8,
		BlobStoreBytes:        1024 * 1024 * 1024, // 1 GiB
		PerAgentBlobBytes:     256 * 1024 * 1024,  // 256 MiB
		MaxFilerefHash:        128,
		DedupPerAgent:         256,
		DedupWindow:           24 * time.Hour,
		AgentTTL:              5 * time.Minute,
		IdleTTL:               45 * time.Minute,
		StaleGrace:            30 * time.Minute,
		DormancyMax:           30 * 24 * time.Hour,
		ArchiveRetention:      7 * 24 * time.Hour,
		ConsumedRetention:     15 * time.Minute,
		ClaimLease:            15 * time.Minute,
		ClaimMaxLife:          24 * time.Hour,
		DefaultDeadline:       10 * time.Minute,
		MaxDeadline:           2 * time.Hour,
		MaxDeadlineDormant:    7 * 24 * time.Hour,
		BlobGraceWindow:       10 * time.Minute,
		BlobTTL:               7 * 24 * time.Hour,
	}
}

// Slot is a public, owner-writable description of a unit of work.
type Slot struct {
	ID   string   `json:"id"`
	Text string   `json:"text"`
	Dirs []string `json:"dirs,omitempty"`
	// Refs are the OBJECTIVE ids this work pursues. "pr:1186", "gate:typos",
	// "issue:1140", "goal:green-main". Two agents sharing a ref are probably
	// duplicating effort, which is the failure Dibs exists to catch. Paths are
	// only a weak hint; objectives are the real key.
	Refs []string `json:"refs,omitempty"`
	// Predicted is THIS declaration's own recorded footprint, kept per slot
	// rather than only merged into the agent's.
	//
	// Matching used to compare a newcomer against an agent's accumulated union, and
	// that union only ever grows: every join folds another member's files in at
	// max weight, and leaving never removes them. So the target got easier the
	// longer an agent lived, and an agent that matched more gained members and gained
	// surface by gaining them. Measured: the same unrelated newcomer scored 0.0000
	// against a one-member agent and 0.1000 against the same agent with five,
	// crossing a real fleet's join bar without its work changing or the agent's
	// topic changing.
	//
	// Keeping it here makes the comparison slot-to-slot: one live declaration
	// against another live declaration, which is the thing an agent can actually
	// be duplicating. The agent's merged footprint stays for candidate generation,
	// where breadth is a virtue.
	Predicted []PredFile `json:"predicted,omitempty"`
	// Activity is WHAT this agent is doing to the work, as opposed to which work
	// it is: implement, review, test, investigate, document, release.
	//
	// Without it, two agents attached to the same PR are indistinguishable from
	// each other, and the strongest evidence Dibs has: a shared identifier,
	// produces the worst possible advice. An implementer and a REVIEWER on
	// pr:1231 both classify as "the same work item", so the reviewer is told to
	// stand down from reviewing because somebody else is attached to the thing it
	// is reviewing. That is not a duplicate; it is the process working.
	//
	// Free-form and optional, because a field agents will not fill is worse than
	// none. Unknown means unknown: it never contradicts anything, and two unknowns
	// are not evidence of agreement.
	Activity string `json:"activity,omitempty"`
	// Holds are exclusive HOST resources this work needs: "port:8080",
	// "lock:.git/index", "gpu:0", "cache:cargo", "service:postgres".
	//
	// The dimension Dibs was blind to, and the most Dibs-specific one there is.
	// Dibs exists because these agents share a MACHINE, and it modelled only the
	// code they share. Two agents running the test suite both bind :8080; two
	// running git in one worktree both take .git/index.lock; two building both
	// want the same cargo cache. None of that is repository surface, none of it
	// shows up in a declaration's prose, and every one of them is a hard failure
	// rather than a merge conflict: the second agent does not get a confusing
	// diff, it gets "address already in use" and no idea why.
	//
	// Reported by an adversarial review as an entire missing axis, correctly.
	Holds         []string `json:"holds,omitempty"`
	UpdatedSerial uint64   `json:"updated_serial"`
}

// Complementary reports whether two activities are different ROLES on one piece
// of work rather than two agents doing the same job.
//
// Deliberately narrow. Only pairs that are unambiguously different roles count;
// anything unknown, equal, or unrecognised is not complementary, because guessing
// "you two are fine" is the failure mode that lets a real duplication through.
func Complementary(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" || a == b {
		return false
	}
	role := map[string]string{
		"implement": "write", "build": "write", "fix": "write", "refactor": "write",
		"review": "check", "test": "check", "audit": "check", "verify": "check",
		"investigate": "study", "document": "study", "design": "study",
		"release": "ship", "deploy": "ship",
	}
	ra, oka := role[a]
	rb, okb := role[b]
	return oka && okb && ra != rb
}

// AgentInfo is who is behind an agent, for the human reading the board.
// Descriptive only: none of it grants anything, so a wrong value misleads a
// reader and cannot escalate.
//
// Agent is an agent's registration. Replayable fields only: last-seen
// freshness and process-aliveness are presentation annotations computed by
// the engine (SPEC §2) and never appear here.
// AgentInfo is who is behind an agent, for the human reading the board. Purely
// descriptive: none of it grants anything, so a wrong value misleads a reader
// and cannot escalate.
type AgentInfo struct {
	Harness  string `json:"harness,omitempty"`  // "Claude Code", "Codex": from clientInfo
	Version  string `json:"version,omitempty"`  // harness version, from clientInfo
	Surface  string `json:"surface,omitempty"`  // "claude-desktop", "cli": entrypoint, when known
	Model    string `json:"model,omitempty"`    // self-reported; no harness sends this
	Provider string `json:"provider,omitempty"` // self-reported or PI_PROVIDER
	Effort   string `json:"effort,omitempty"`   // reasoning effort, when the harness exposes it

	// Where the agent is working. In a fleet these answer "which of my sessions
	// is this?" faster than any id: Title is what the human named the session,
	// Branch and CWD say which code it is touching, Host which machine.
	//
	// Project is which codebase, resolved from CWD by the caller and RECORDED
	// here rather than derived on read. Deriving it needs Git, and core is pure;
	// recording the resolved value is the same bargain every other impure input
	// on an Op makes, and it means replay reproduces the label the fleet
	// actually saw rather than whatever the tree looks like today.
	//
	// A LABEL, for a human scanning rows. Never match, group or authorise by it:
	// two unrelated clones are both called "api", and only paths.SameRepo is
	// entitled to say whether two directories are one project.
	Title   string `json:"title,omitempty"`
	CWD     string `json:"cwd,omitempty"`
	Project string `json:"project,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Host    string `json:"host,omitempty"`

	// The repository this agent is in, as identity rather than as a label.
	// RepoDir is Git's common directory (shared by every linked worktree of one
	// repository); RepoRemote is the normalized primary remote.
	//
	// Unlike Project, these DO decide things: a ref like "issue:42" means
	// something only inside one repository, so an objective shared with an agent
	// in a different project is a coincidence rather than duplicated effort.
	// Recorded rather than derived because the comparison happens in the fold,
	// which cannot call Git, and because a replay must reach the same verdict on
	// a machine where the checkout is long gone.
	// RepoRoots is the repository's parentless commits, sorted and space joined.
	// It settles the one case the other two cannot: a clone whose origin was
	// removed looks exactly like a repository created locally, and one of those
	// is the same project while the other is a stranger. Shared history is the
	// only evidence that separates them.
	RepoDir    string `json:"repo_dir,omitempty"`
	RepoRemote string `json:"repo_remote,omitempty"`
	RepoRoots  string `json:"repo_roots,omitempty"`
}

// Agent is a participant on the board: an identity, a mailbox and a heartbeat.
// (SPEC-CHANNELS.md §1 renames this to `agent`; the rename is a separate pass.)
type Agent struct {
	ID          string    `json:"id"`
	Kind        AgentKind `json:"kind"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	PID         int       `json:"pid,omitempty"`
	ProcStart   int64     `json:"proc_start,omitempty"` // unix ms; defeats PID reuse
	Status      AgentStatus
	// SessionID binds the agent to its harness session, so a lifecycle hook that
	// knows only "${session_id}" can find the right mailbox without carrying a
	// token through config. Set at registration; never a credential on its own,
	// the connection is already authenticated.
	SessionID string `json:"session_id,omitempty"`
	// SessionAliases are the OTHER names this same session is known by, because
	// the two halves of a harness do not always agree on one.
	//
	// The stdio bridge derives `host-<ppid>` from the process that spawned it,
	// which is what an in-process plugin can also observe. A harness whose hooks
	// are configured rather than in-process sends what IT calls the session
	// instead: Codex passes a uuid. Both are truthfully the same session, and
	// before this an agent could answer to exactly one of them, so mail was
	// delivered to a name its own Stop hook never used and nothing was ever
	// woken. Neither id is a credential; the connection is already
	// authenticated, and these are only ever added by the daemon's own join.
	SessionAliases []string `json:"session_aliases,omitempty"`
	// GuessedSessions are the session ids on this agent that the daemon
	// INFERRED by directory rather than the caller stating them. A guess yields
	// to a first-hand claim; a stated binding does not. See Op.SessionGuessed.
	//
	// A SET, NOT A BOOLEAN, and the difference is a hole rather than a detail.
	// One flag per agent was overwritten by whichever binding happened last, so
	// adding a single guessed ALIAS to an agent made its STATED primary
	// claimable by anyone, and adding a later stated alias made an earlier guess
	// permanently non-yielding. Authorisation asks about one specific id, so the
	// provenance has to be recorded against that id.
	GuessedSessions []string `json:"guessed_sessions,omitempty"`
	// Agent is who is behind this agent: harness, version, model, surface. In a
	// large fleet "reviewer" is not enough; the human needs to know that it is
	// Codex 0.145 rather than Opus 5 in Claude Desktop. Purely descriptive: it
	// never grants anything, so a wrong value misleads a reader but cannot
	// escalate. Harness/version come from the MCP handshake (the client states
	// them, not the agent); model is self-reported because no harness puts it
	// on the wire.
	Agent *AgentInfo `json:"agent,omitempty"`
	// Parent is the agent that spawned this one, when it declared one.
	//
	// Spawning subagents is ordinary development behaviour and MUST NOT require
	// coordination ceremony (SPEC-CHANNELS.md §8.2): a subagent inherits its
	// parent's agent membership, does not join, does not queue, and is not
	// counted as a second occupant. The parent stays accountable: its departure
	// takes the subagent's access with it, because the access was never the
	// subagent's to begin with.
	Parent string `json:"parent,omitempty"`

	// ParentProven records that the named parent VOUCHED for this child.
	//
	// Parent arrives as a bare string on the wire and nothing checked it. The
	// powers keyed off it are not cosmetic: a subagent speaks under its
	// parent's membership, skips an exclusive space's queue, and is exempt from
	// its parent's exclusive claims in the guard. Verified against a running
	// daemon: an agent registering with parent:"victim" posted into the
	// victim's exclusive space, joined it instead of queueing, and got
	// allow/no-claim for a path the victim held exclusively.
	//
	// So lineage is now claimed and PROVEN separately. An unproven parent stays
	// on the board as the lineage the agent asserts: useful for a human
	// reading a fleet, and grants nothing.
	ParentProven bool `json:"parent_proven,omitempty"`

	// ChildNonces are one-time secrets this agent has issued to subagents it is
	// spawning, each burned on first use.
	//
	// One-time because a reusable one is a standing capability: leak it once
	// and every future holder inherits this agent's memberships and its guard
	// exemption. Bounded because an agent that issues and never spawns must not
	// grow the state without limit.
	ChildNonces map[string]bool `json:"-"`

	// Role is "member", "coordinator", or "admin". Only the human, through the
	// admin path, can grant it: an agent can never promote itself.
	Role          string `json:"role,omitempty"`
	CreatedSerial uint64 `json:"created_serial"`
	// AckedSerial is the awareness-gate watermark; 0 = gate not passed in the
	// current activation (cleared by every dormant/stale transition and wake).
	AckedSerial uint64 `json:"acked_serial"`
	// Activation is the generation counter, incremented by each resume.
	Activation uint64 `json:"activation"`
	// LastCoordination is the latest durable coordination checkpoint: a
	// conservative lower bound on the agent's own last accepted call (may
	// trail by up to TTL/2). Updated only by ops with this agent as actor.
	LastCoordination time.Time `json:"last_coordination_at"`
	StaleSince       time.Time `json:"stale_since,omitzero"`
	DormantSince     time.Time `json:"dormant_since,omitzero"`
	ArchivedAt       time.Time `json:"archived_at,omitzero"`
	// TruncatedBefore is the mailbox-loss watermark: mail with serial below
	// it may have been evicted unconsumed (SPEC §8).
	TruncatedBefore uint64          `json:"truncated_before_serial,omitempty"`
	Slots           map[string]Slot `json:"slots,omitempty"`

	// StaleReason is WHY this agent stopped counting as live, recorded at the
	// moment it transitioned.
	//
	// The reason was already computed there and put only into the `agent.stale`
	// event, so a human opening the board later saw "out of touch" and nothing
	// else: next to a last-contact time of "now", which reads as the board
	// being broken rather than the agent being dead. The three cases are not
	// interchangeable: a process that exited is definitive, a lapsed lease may
	// just be a long build, and an agent that never gave a PID has told us
	// nothing about a process at all.
	//
	// Replay-safe: every input to it (DeadAgents, StaleAgents, the agent's own
	// PID) is recorded in the sweep op, never probed during Apply.
	StaleReason string `json:"stale_reason,omitempty"`

	Token string `json:"-"`
	Nonce string `json:"-"`
}

// burnChildNonce reports whether n is a secret this agent issued, consuming it.
//
// Consuming is the point: a proof that can be replayed is a capability, and a
// capability that grants another agent's guard exemption should not be
// re-presentable by whoever sees it next.
func (l *Agent) burnChildNonce(n string) bool {
	if l == nil || n == "" || !l.ChildNonces[n] {
		return false
	}
	delete(l.ChildNonces, n)
	return true
}

// maxChildNonces bounds outstanding vouchers. An agent that issues and never
// spawns must not grow state without limit.
const maxChildNonces = 32

// Sleeping reports whether the agent is in its kind's lease-lapsed state.
func (l *Agent) Sleeping() bool {
	return l.Status == StatusStale || l.Status == StatusDormant
}

// finishedCleanly reports that the agent ended on purpose rather than going
// dark: it called sign_off, or it did and was later retired by retention.
//
// The distinction matters to anyone still waiting on it: a deliberate close
// released every claim and answered nothing further BY CHOICE, while a lapsed
// lease means the work may still be running somewhere unobserved. Reporting the
// first as the second sends people to verify directories that were cleanly let
// go: the opposite of the caution SPEC §7's honest-liveness rule exists for.
//
// StaleReason is the discriminator, and it survives archiving: it is set only
// when the sweep declared the agent dark, so an archived agent with none was
// archived after a clean close.
func (l *Agent) finishedCleanly() bool {
	if l == nil {
		return false
	}
	return (l.Status == StatusClosed || l.Status == StatusArchived) && l.StaleReason == ""
}

// Gone reports whether an agent is finished with: closed by itself, or
// archived by retention. A nil agent counts: it was pruned out from under us.
//
// Distinct from Sleeping, and the distinction is the whole point. A stale agent
// crashed and a dormant one is asleep; both may come back, so both keep their
// memberships and their places in queues. A gone agent never comes back, and
// carrying it forward is litter.
//
// Written down because the two were being conflated by hand: several checks
// tested Status == StatusClosed alone, which quietly answered "still here" for
// an archived agent AND for a crashed one, and that second answer handed
// exclusive ownership of an agent to an agent the sweep had already declared
// dead.
func (l *Agent) Gone() bool {
	return l == nil || l.Status == StatusClosed || l.Status == StatusArchived
}

// CanHoldExclusive reports whether an agent is in a state where handing it an
// exclusive lock would actually coordinate anything. Only an ACTIVE agent is:
// a sleeping one blocks everybody while it is not working, and a gone one
// blocks them forever.
func (l *Agent) CanHoldExclusive() bool {
	return l != nil && l.Status == StatusActive
}

// Message is one mailbox item. Body/Response plaintext in memory; ciphertext
// at rest.
type Message struct {
	Serial      uint64    `json:"serial"`
	From        string    `json:"from"`
	To          string    `json:"to"`
	Type        string    `json:"type"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	Consumed    bool      `json:"consumed"`
	Deadline    time.Time `json:"deadline,omitzero"`
	Response    string    `json:"response,omitempty"`
	DeliveredAt uint64    `json:"delivered_serial,omitempty"`
	// SentAt and DeliveredTime are wall-clock, and they exist so an agent can
	// tell what happened to it.
	//
	// The serials above order events; they cannot answer "was I woken by this, or
	// did it sit in a queue until I happened to look?" An agent asked for exactly
	// this after being reached mid-restart: "I cannot tell from inside whether it
	// woke me or queued. Put delivered_at and read_at in the envelope and the gap
	// answers it."
	//
	// DeliveredTime IS the read time: a message becomes delivered when its
	// recipient pulls it with inbox or check_in, so the gap from SentAt is how
	// long it waited. There is no separate read_at, because a second stamp written
	// on a pure read path would be an unledgered mutation: the bug class that
	// once put a hole in a real board's serial sequence.
	SentAt        time.Time    `json:"sent_at,omitzero"`
	DeliveredTime time.Time    `json:"delivered_at,omitzero"`
	RespondedAt   uint64       `json:"responded_serial,omitempty"`
	AckedAt       uint64       `json:"acked_serial,omitempty"`
	TerminalAt    time.Time    `json:"terminal_at,omitzero"` // when it reached a terminal state
	ExpireDetail  string       `json:"expire_detail,omitempty"`
	Attachments   []Attachment `json:"attachments,omitempty"` // blob handles + filerefs (A2)
	// Choices is the answer space of a question, stated by its sender. Ledgered
	// with the message because it is part of what was ASKED: a question whose
	// options were lost on replay is a different question.
	Choices []string `json:"choices,omitempty"`
	// Grant is the role this request asks for. Ledgered with the message because
	// approving the message is what performs the grant: a request replayed
	// without it would be an approval of nothing.
	Grant string `json:"grant,omitempty"`
	// Adopt is the abandoned agent this request asks to reclaim, for the same
	// reason and with the same consequence on approval.
	Adopt string `json:"adopt,omitempty"`
}

// Terminal implements the exact SPEC §8 predicate, used consistently by
// capacity, displacement, inbox, retention, and GC.
func (m *Message) Terminal() bool {
	switch m.State {
	case MsgStateAnswered, MsgStateApproved, MsgStateDenied, MsgStateDeclined,
		MsgStateExpiredSilent, MsgStateExpiredDormant, MsgStateExpiredDead,
		MsgStateDisplaced:
		return true
	case MsgStateAcked:
		return m.Type == MsgNotify || m.Type == MsgHandoff
	}
	return false
}

// Expecting reports whether the message type awaits a response.
func (m *Message) Expecting() bool {
	return m.Type == MsgQuestion || m.Type == MsgRequest
}

// Claim is an advisory, TTL-leased declaration over a path prefix.
type Claim struct {
	Agent          string    `json:"agent"`
	Path           string    `json:"path"`
	Mode           string    `json:"mode"`
	Note           string    `json:"note,omitempty"`
	AcquiredSerial uint64    `json:"acquired_serial"`
	Acquired       time.Time `json:"acquired"`
	Renewed        time.Time `json:"renewed"`
}

// DedupRec is one identified-op record (SPEC §4): bounded by the lesser of
// DedupWindow and DedupPerAgent, digest-bound against payload reuse.
type DedupRec struct {
	Agent      string    `json:"agent"`
	ID         string    `json:"id"` // op_id or resume_id
	Digest     string    `json:"digest"`
	Serial     uint64    `json:"serial"` // msg_serial for sends
	Activation uint64    `json:"activation"`
	Token      string    `json:"-"` // resume records: the rotated token
	At         time.Time `json:"at"`
}

// Event is one entry in the event feed; (Serial, Sub) totally ordered.
type Event struct {
	Serial uint64         `json:"serial"`
	Sub    int            `json:"sub"`
	TS     time.Time      `json:"ts"`
	Type   string         `json:"type"`
	Agent  string         `json:"agent,omitempty"`
	To     string         `json:"to,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

// State is the entire replayable truth. Only Apply mutates it.
type State struct {
	NodeID   string
	Serial   uint64
	Limits   Limits
	Agents   map[string]*Agent
	Messages map[uint64]*Message // keyed by send serial
	Claims   []*Claim
	Nonces   map[string]string    // nonce → agent_id
	Dedup    map[string]*DedupRec // key: agent_id + "\x00" + id
	Blobs    map[string]*Blob     // id → registry entry (bytes live in blobstore)

	// Spaces of work, and the announcements awaiting acknowledgement in them
	// (SPEC-CHANNELS.md). Keyed by space id and by announce serial.
	Spaces        map[string]*Space
	Announcements map[uint64]*Announcement
}

// NewState returns an empty state for a node.
func NewState(nodeID string, lim Limits) *State {
	return &State{
		NodeID:   nodeID,
		Limits:   lim,
		Agents:   map[string]*Agent{},
		Messages: map[uint64]*Message{},
		Nonces:   map[string]string{},
		Dedup:    map[string]*DedupRec{},
		Blobs:    map[string]*Blob{},

		Spaces:        map[string]*Space{},
		Announcements: map[uint64]*Announcement{},
	}
}

// AgentByToken resolves an auth token, or nil. Constant-time comparison per
// candidate; the map walk is bounded by MaxAgents.
func (s *State) AgentByToken(tok string) *Agent {
	if tok == "" {
		return nil
	}
	for _, l := range s.Agents {
		if l.Token != "" && constEq(l.Token, tok) && l.Status != StatusArchived {
			return l
		}
	}
	return nil
}

// Inbox returns the agent's non-terminal plus unconsumed-terminal messages,
// oldest first (SPEC §8).
func (s *State) Inbox(agent string) []*Message {
	var out []*Message
	// THE WATERMARK FILTERS, and until now it only reported.
	//
	// TruncatedBefore says "mail below this is not mine". It was set by the
	// retention sweep and returned to callers as truncated_before_serial, and
	// nothing ever consulted it, which went unnoticed because the sweep that
	// sets it has already DELETED the messages it covers: there was nothing left
	// to filter, so an inert watermark and a working one looked identical.
	//
	// They stop looking identical when mail outlives the row it was addressed
	// to. An id is derived from the name, so a name that comes back reuses the
	// id, and a sweep written before v0.0.7 removes the row while keeping the
	// messages. Those are expired with a reason the SENDER reads, so they are
	// deliberately kept, and the next agent to take that name was handed them,
	// bodies included. Measured. Found by the pre-release review.
	floor := s.mailFloor(agent)
	for _, m := range s.Messages {
		if m.Serial < floor {
			continue
		}
		if m.To == agent && m.readable() {
			out = append(out, m)
		}
	}
	sortMessages(out)
	return out
}

// readable reports whether this message would appear in its addressee's inbox:
// still live, or finished and not yet collected (SPEC §8).
//
// One definition, because adoption had a second one. It readdressed and COUNTED
// every record above the watermark, consumed ones included, and told the heir
// to "read them with inbox": a mailbox with one unread message and one
// acknowledged one reported two and showed one. A count handed to a person
// approving the request has to be the number they would see.
func (m *Message) readable() bool { return !m.Terminal() || !m.Consumed }

func sortMessages(ms []*Message) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j-1].Serial > ms[j].Serial; j-- {
			ms[j-1], ms[j] = ms[j], ms[j-1]
		}
	}
}

// mailFloor is the serial below which mail addressed to this agent is not the
// agent's own. See Agent.TruncatedBefore.
//
// One function because "is this message mine" is asked in more than one place
// and was answered differently in each. Inbox filtered on the watermark; the
// capacity metric and the delivery marker did not, and both of those decide
// something a SENDER is told.
func (s *State) mailFloor(agent string) uint64 {
	if l := s.Agents[agent]; l != nil {
		return l.TruncatedBefore
	}
	return 0
}

// nonTerminalCount is the mailbox-capacity metric (SPEC §8).
//
// It honours the watermark, which it did not. Mail below it belongs to a
// previous occupant of this name: the current agent cannot see it, so it cannot
// read, answer, ack or consume it, and nothing it does will ever retire it.
// Counting it against capacity meant a send could be refused with
// E_MAILBOX_FULL against an agent whose inbox reads as empty, with no
// corrective action available to either party: the recipient cannot clear what
// it cannot see, and the sender is simply refused. Rule 6 says an error names
// the corrective call, and this one had none to name. Reproduced before fixing:
// two pending notifies to a swept name, the name returns, and a question to it
// is refused forever.
func nonTerminalCount(s *State, agent string) int {
	n := 0
	floor := s.mailFloor(agent)
	for _, m := range s.Messages {
		if m.To == agent && m.Serial >= floor && !m.Terminal() {
			n++
		}
	}
	return n
}

func constEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// MaxChoices bounds the answers a question may enumerate.
//
// Four, because the point of stating them is to make answering a press rather
// than a composition, and a list long enough to need scrolling has given that
// up. It is also what the delivery surfaces can render: a macOS notification
// carries three buttons plus the implicit dismiss, so a question that fits here
// is answerable in one gesture wherever it lands.
const MaxChoices = 4
