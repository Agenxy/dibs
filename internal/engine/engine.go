// Package engine runs the single-writer event loop: every mutation and read
// from every transport executes sequentially in one goroutine over the pure
// core. Request phases (SPEC §2): transport/auth → structural → rate →
// domain; a call passing 1–3 wakes a sleeping agent via a ledgered wake
// even if phase 4 rejects. The engine ledgers exactly the ops that advanced
// the serial: the two cannot disagree by construction.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/overlap"
)

// Engine owns all state. Public methods are safe for concurrent use.
type Engine struct {
	ops      chan request
	subs     chan subReq
	unsubs   chan chan core.Event
	state    *core.State
	led      Ledger
	blobs    Store
	prober   Prober
	ring     []core.Event
	ringCap  int
	buckets  map[string]*bucket
	resumeAt map[string]time.Time // per-agent resume rate limit (1/10s)
	watch    []waiter
	streams  map[chan core.Event]bool
	// seen: ephemeral lease freshness (reads/heartbeats). Never replayed;
	// folded into recorded sweep decisions (SPEC §2 tier 2).
	seen map[string]time.Time

	// announceSent throttles announcement redelivery, keyed "agent\x00serial".
	//
	// EPHEMERAL, deliberately. An announcement is replayable coordination state;
	// when it was last shown to somebody is presentation timing, and writing that
	// into the ledger from a read path would be an unledgered mutation: the
	// exact class of bug that put a hole in a real board's serial sequence.
	// Losing this on restart is harmless: the worst case is one extra reminder
	// about something the agent genuinely has not acknowledged. Same category as
	// `seen`.
	announceSent map[string]time.Time
	// wokeFor throttles the WAKE itself, keyed "agent\x00serial".
	//
	// An agent must learn about mail when it arrives, not when a human next
	// types: that is the whole of situational awareness, and a fleet that waits
	// for a person to kickstart its responsiveness is not agentic. So the wake
	// fires for anything unread.
	//
	// What it must not do is fire for the SAME thing every turn. An agent that
	// read a message and chose not to act on it has decided, and re-waking it
	// each turn would be nagging an agent for exercising the judgement the
	// digest explicitly grants it. So each message wakes once, and only work
	// somebody is blocked on comes back, on the same retry the announcement
	// reminder uses.
	wokeFor map[string]time.Time
	// announceTries counts how many times each reminder has been shown, keyed
	// the same way. Ephemeral for the same reason: it is delivery bookkeeping,
	// and the decision it feeds is recorded in the sweep op.
	announceTries map[string]int

	// Work-overlap scoring (SPEC-CHANNELS.md). Guarded by its own mutex rather
	// than the loop: Predict runs OFF the writer goroutine, because a model that
	// takes a second would otherwise stall every other agent on the board.
	matchMu sync.RWMutex
	scorer  overlap.Scorer
	// scorers is one index per repository root. A co-change model only means
	// anything inside the history it was mined from, so an agent is scored by
	// the tree it is actually working in.
	scorers  map[string]overlap.Scorer
	matchCfg MatchConfig
	// onRepoSeen lets the daemon index the repositories agents actually work
	// in, so matching does not depend on somebody setting a flag.
	onRepoSeen func(repo string)
	// verifyClaim answers whether a presented coordinator claim is the one this
	// daemon minted, and returns the function that spends it once the op it
	// authorised has been ledgered. Guarded separately: it is installed once at
	// startup and read on the loop.
	claimMu     sync.RWMutex
	verifyClaim func(secret string) (bool, func())
	// footprints backfills agents opened before the index was ready. Ephemeral:
	// it is a cache of a prediction, never the record a join is replayed from.
	footprints map[string][]core.PredFile

	// wake is the operator's policy for which news may extend an agent's turn.
	// Guarded because it is read on every lifecycle hook and written once at
	// startup, from a different goroutine.
	wake wakeState

	// matchStatus is why matching did or did not do anything, so a declaration
	// never comes back silently ambiguous. See matchstatus.go.
	matchStatus matchStatusState

	// notices are state changes an agent did not cause and must be told about,
	// admitted by a director, promoted from a queue, evicted. Ephemeral; see
	// notices.go.
	notices map[string][]notice

	// children are agents this machine's harnesses spawned, as reported by
	// their own lifecycle hooks. Ephemeral: which processes are running is an
	// observation about now, not something replaying the ledger should
	// reproduce. See supervision.go.
	children map[string]Child

	// hooks records whether harness integrations are actually resolving to
	// agents: the difference between a guard that is protecting nothing and a
	// board where nothing is claimed. See hookhealth.go.
	hooks hookHealth

	// human is the operator's own agent, so a person can join agents and speak in
	// them through the same tools an agent uses. See human.go.
	human humanState
	// What Dibs has already told the coordinator about itself, so a fault that
	// recurs every sweep does not become a message every sweep.
	faults faultState
}

type request struct {
	op    *core.Op
	fn    func() core.Result
	reply chan reply
}

type reply struct {
	res core.Result
	err error
}

type waiter struct {
	since   uint64
	agent   string
	all     bool
	ch      chan []core.Event
	expires time.Time
}

type subReq struct {
	ch    chan core.Event
	since uint64
}

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	rateOpsPerSec = 10
	rateBurst     = 30
)

// New assembles an engine over a replayed state and open ledger. history, when
// given, seeds the event ring so cursors survive a restart.
func New(st *core.State, led Ledger, prober Prober, history ...[]core.Event) *Engine {
	var ring []core.Event
	if len(history) > 0 {
		ring = history[0]
	}
	return &Engine{
		ring: ring,
		ops:  make(chan request), subs: make(chan subReq), unsubs: make(chan chan core.Event),
		state: st, led: led, prober: prober,
		ringCap: 65536, buckets: map[string]*bucket{},
		resumeAt: map[string]time.Time{},
		streams:  map[chan core.Event]bool{}, seen: map[string]time.Time{},
		announceSent: map[string]time.Time{}, announceTries: map[string]int{},
		wokeFor: map[string]time.Time{},
	}
}

// Run drives the loop until ctx is done. Call in exactly one goroutine.
func (e *Engine) Run(ctx context.Context) {
	e.boot(time.Now())
	e.reconcileBlobs() // startup reconcile: drop crash orphans (A4.1)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	reconcileTick := time.NewTicker(30 * time.Second)
	defer reconcileTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reconcileTick.C:
			e.reconcileBlobs() // delete evicted/orphan blob files off-thread
		case req := <-e.ops:
			if req.fn != nil {
				res := req.fn()
				if errv, ok := res["error"].(error); ok {
					req.reply <- reply{nil, errv}
				} else {
					req.reply <- reply{res, nil}
				}
				continue
			}
			res, err := e.exec(req.op, time.Now())
			req.reply <- reply{res, err}
		case now := <-tick.C:
			e.sweep(now)
			e.expireWaiters(now)
		case s := <-e.subs:
			e.streams[s.ch] = true
			// Catch-up replay, deliberately best-effort: the `default` drops
			// events once the subscriber's buffer is full rather than blocking.
			//
			// Blocking here would stall the SINGLE WRITER on a slow reader,
			// one wedged browser tab would freeze the whole fleet, so dropping
			// is the only correct policy. It is safe because the SSE stream
			// carries a full board SNAPSHOT in every frame: a subscriber that
			// misses replayed events still renders correct state, and the only
			// loss is rows in the on-screen activity list. The ledger, which is
			// the actual record, is untouched.
			//
			// Written down because a bare `default` reads like an oversight, and
			// the next person to see it should not "fix" it into a block.
			for _, ev := range e.eventsSince(s.since, "", true) {
				select {
				case s.ch <- ev:
				default:
				}
			}
		case ch := <-e.unsubs:
			delete(e.streams, ch)
			close(ch)
		}
	}
}

// boot applies SPEC §7's evidence rule: grace to boot+TTL only for agents whose
// durable coordination checkpoint is within one TTL; the rest transition now,
// ledgered, healed later by wake if the agent lives.
func (e *Engine) boot(now time.Time) {
	op := &core.Op{Kind: core.OpSweep}
	for id, l := range e.state.Agents {
		if l.Status != core.StatusActive {
			continue
		}
		if now.Sub(l.LastCoordination) <= e.state.Limits.AgentTTL {
			e.seen[id] = now // one TTL of boot grace, evidence-backed
		} else {
			op.StaleAgents = append(op.StaleAgents, id)
		}
	}
	if len(op.StaleAgents) > 0 {
		_, _ = e.applyAndLedger(op, now)
	}
}

// exec runs the request phases for a mutating op.
func (e *Engine) exec(op *core.Op, now time.Time) (core.Result, error) {
	op.AgentID = "" // replay-only actor field; never trusted from ingress
	// Same rule: the claim VERDICT is the engine's to record, never the
	// caller's to assert. Checked below, after the actor is known.
	op.ClaimVerified = false
	op.AdoptAuthorised = false

	// Ingress-only validation. Deliberately NOT inside Apply: Apply is also the
	// fold that replays the ledger, so a rule added there binds history
	// retroactively and a daemon can refuse to replay ops it wrote itself.
	// See core.Admit.
	if err := core.Admit(op, e.state.Limits); err != nil {
		return nil, err
	}

	// The operator's own agent is not registrable from a tool call.
	//
	// THE HOLE THIS CLOSES, reproduced end to end before it was written: the
	// human's recovery nonce was `human:<OS username>`, and registration
	// reattaches on a matching nonce and hands back the token. So any agent
	// that could run `whoami` could become the person at the keyboard, and then
	// approve its own coordinator grant. Touch ID protects the board session
	// and was never reached, because approving a request never asks for it.
	//
	// It has to be HERE. `internal/core` has no notion of the human, so the
	// fold cannot tell that row from any other, and a rule added to Apply would
	// bind every register op already in the ledger, including the legitimate
	// ones that minted the identity in the first place. This is the same
	// placement, and the same reasoning, as ClaimVerified and AdoptAuthorised
	// directly above.
	if !op.HumanMint && e.wouldTakeHumanIdentity(op) {
		return nil, core.ErrHumanIdentity
	}

	// `to: "coordinator"` reaches whoever holds the role.
	//
	// An agent asking for its identity back does not know, and should not have
	// to look up, which of sixteen rows is the coordinator today. The role is
	// the address; the id is an implementation detail that changes when somebody
	// hands the role over. Resolved at ingress, so the LEDGER records the agent
	// it actually went to: a message addressed to a role, replayed after the
	// role moved, would otherwise be delivered to somebody it was never sent to.
	if op.Kind == core.OpSendMessage && op.To == core.RoleCoordinator {
		who := e.state.CoordinatorID()
		if who == "" {
			return nil, core.ErrNoCoordinator
		}
		op.To = who
	}

	// An agent whose bridge cannot see the harness session id adopts the one
	// its harness announced from the same directory.
	//
	// Resolved at ingress and written INTO the op, like the two above, so the
	// ledger records the session the agent actually holds and replay reaches the
	// same board without consulting a children map that no longer exists.
	// See sessionjoin.go for why this is the register path and not hook_poll.
	// A harness that NAMED its own thread is believed over anything inferred,
	// once it is checked. Codex sends `threadId` on every tool call, and it is
	// the id `codex resume` takes, so this is the identity rather than a
	// correlation. Vetted, not trusted: see mayClaimSession.
	if claimed := op.SessionAlias; claimed != "" {
		if !e.mayClaimSession(claimed, op.Token) {
			op.SessionAlias = ""
		}
	}
	if op.SessionAlias == "" {
		switch op.Kind {
		case core.OpRegister, core.OpUpdate:
			if op.Agent != nil {
				op.SessionAlias = announcedSession(e.children, e.state, op.Agent.CWD, now)
			}
		case core.OpAckBoard:
			// check_in carries no cwd, so the agent's own registered one is used.
			// This is the path that reaches agents ALREADY on the board: they
			// have no reason to register again, and check_in is the one call
			// they all keep making.
			if l := e.state.AgentByToken(op.Token); l != nil && l.Agent != nil {
				op.SessionAlias = announcedSession(e.children, e.state, l.Agent.CWD, now)
			}
		default:
			op.SessionAlias = "" // no other op binds an identity
		}
	}

	// A request to a PERSON gets a person's deadline.
	//
	// The default is ten minutes, which is right for an agent: it is in a loop,
	// it answers in seconds, and a stale question should expire rather than
	// linger. A human is not in a loop. That is the premise the whole product
	// rests on, and this one default contradicted it.
	//
	// Measured on this board, on the request that would have made the
	// maintainer a coordinator: sent, delivered, never seen because a Focus mode
	// swallowed the notification, and expired thirty minutes later as
	// `expired_recipient_dormant` while the operator was away from the machine.
	// The feature worked. The clock was set for somebody else.
	//
	// Recorded into the op rather than decided at read time, which is the rule
	// for every impure input here: replay must reach the same deadline, and who
	// the human is can change.
	if op.Kind == core.OpSendMessage && op.DeadlineSec == 0 && op.MsgType != core.MsgNotify {
		if human := e.humanIdentityLocked(); human != "" && op.To == human {
			op.DeadlineSec = int(humanDeadline / time.Second)
		}
	}

	// A role is a human's to give.
	//
	// core validates WHICH roles may be requested and on what message type; it
	// cannot validate the recipient, because core does not know that humans
	// exist and that rule is what keeps it a pure state machine. So the half
	// that needs the identity lives here, next to the identity.
	//
	// Without it, two agents could promote each other by exchanging requests and
	// pressing each other's buttons, which is self-promotion with one extra
	// participant. Refused at ingress, so nothing is ledgered.
	if op.Kind == core.OpSendMessage && op.Grant != "" {
		if human := e.humanIdentityLocked(); human == "" || op.To != human {
			return nil, core.ErrGrantNeedsHuman
		}
	}

	// System ops (engine-generated) bypass phases.
	// System ops carry no agent token: they are generated by the engine itself,
	// or (grant_role) admitted only on the daemon's admin path, which the HTTP
	// gate has already authenticated with the local secret + admin password.
	system := op.Kind == core.OpSweep || op.Kind == core.OpMarkDelivered || op.Kind == core.OpWake ||
		op.Kind == core.OpActivityCheckpoint || op.Kind == core.OpGrantRole ||
		op.Kind == core.OpPrune

	// A system op carries no agent token, and one that does is refused.
	//
	// System ops skip authentication because the daemon generates them, or the
	// HTTP admin gate already authenticated them. That made the whole guarantee
	// rest on a negative: grant_role is safe because no tool exposes it. One tool
	// growing a role argument, or one handler forwarding an op it decoded, and an
	// agent promotes itself to admin: a role that reads every other agent's mail.
	//
	// Presenting a token is positive evidence that a REQUEST is being replayed
	// rather than the daemon acting as itself, so it is exactly the thing to
	// refuse. Costs one comparison and removes the class.
	if system && op.Token != "" {
		return nil, core.ErrBadToken
	}

	var actor *core.Agent
	if !system {
		switch op.Kind {
		case core.OpRegister:
			tok, err := newSecret()
			if err != nil {
				return nil, err
			}
			op.NewToken = tok
			// The fleet knows which trees it works in, so matching can index
			// them without anybody configuring a path.
			if op.Agent != nil {
				e.noteRepoOf(op.Agent.CWD)
			}
		case core.OpResume:
			// Phase 3 for resumes: 1/10s per agent, keyed via nonce.
			if id, ok := e.state.Nonces[op.Nonce]; ok {
				if last, seen := e.resumeAt[id]; seen && now.Sub(last) < 10*time.Second {
					return nil, core.ErrRateLimited
				}
				e.resumeAt[id] = now
			}
			tok, err := newSecret()
			if err != nil {
				return nil, err
			}
			op.NewToken = tok
		default:
			// Phase 1: auth. Phase 3: rate. Phase 3.5: ledgered wake.
			actor = e.state.AgentByToken(op.Token)
			if actor == nil {
				return nil, core.ErrBadToken // never wakes, never ledgers
			}
			if !e.allow(actor.ID, now) {
				return nil, core.ErrRateLimited // admitted nothing; no wake
			}
			e.wakeIfSleeping(actor, now)
		}
	}

	// The one impure input this op has: does the caller hold the secret from the
	// daemon's data directory. Answered here, recorded in the op, so replay
	// applies the same decision without re-reading a file that the first
	// successful claim consumed.
	var spendClaim func()
	if op.Kind == core.OpClaimCoordinator {
		e.claimMu.RLock()
		verify := e.verifyClaim
		e.claimMu.RUnlock()
		if verify != nil {
			op.ClaimVerified, spendClaim = verify(op.Nonce)
		}
	}

	// Who may take over an abandoned mailbox. Decided here, where the human's
	// identity is known, and RECORDED, so replay does not have to re-decide it
	// against a board whose roles have since changed.
	if op.Kind == core.OpAdoptAgent && actor != nil {
		op.AdoptAuthorised = e.mayAdopt(actor)
		if err := e.guardHumanMailbox(actor, op.To); err != nil {
			return nil, err
		}
	}
	// Approving an adoption REQUEST needs the same authority as performing one,
	// decided here for the same reason: recorded at ingress, so replay applies
	// the decision that was made rather than re-deciding it against a board
	// whose roles have moved on.
	if op.Kind == core.OpRespond && actor != nil {
		op.AdoptAuthorised = e.mayAdopt(actor)
		if err := e.mayApproveGrant(actor, op); err != nil {
			return nil, err
		}
	}

	res, err := e.applyAndLedger(op, now)
	if err != nil {
		return nil, err
	}
	// After the apply, never before it. Holding the secret is not sufficient:
	// core still refuses an ephemeral or closed claimant, and a single-use claim
	// spent on a refusal leaves the board with no coordinator and no way to
	// appoint one.
	if spendClaim != nil {
		spendClaim()
	}
	if actor != nil {
		e.seen[actor.ID] = now
	}
	if lid, ok := res["agent_id"].(string); ok && lid != "" {
		e.seen[lid] = now
	}
	if op.Kind == core.OpResume {
		if lid, ok := res["agent_id"].(string); ok {
			e.cancelWaiters(lid)
		}
	}
	// check_in is the one moment the AGENT itself has demonstrably read the
	// board (token-authenticated) so it is where notices are DELIVERED and
	// then cleared, rather than on the token-less wake path where a peer that
	// merely knows a session id could consume somebody else's.
	//
	// Delivering here is what makes the wake path safe to interfere with. A
	// peer polling on a timer can keep the nudge quiet indefinitely, because
	// nothing on that path can tell the two callers apart, but the nudge is
	// not the information. Announcements already work this way: the reminder is
	// best-effort, the obligation is read back through the agent's own
	// authenticated call. Notices had no such path, so suppressing the nudge
	// suppressed the fact.
	if op.Kind == core.OpAckBoard && actor != nil {
		// Always present, empty when there is nothing.
		//
		// Omitting the key when nothing had happened meant an agent could not tell
		// "nothing was done to you while you were away" from "this is not working"
		// or "I am asking on the wrong agent". On the tool documented as the
		// recovery checkpoint, reached by an agent that has just lost its context,
		// that ambiguity is the opposite of the reassurance it exists to give,
		// and it was reported as a defect by the first agent to use it that way.
		pending := e.pendingNotices(actor.ID)
		if pending == nil {
			pending = []string{}
		}
		res["agent_updates"] = pending
		e.AckNotices(actor.ID)
		// Whether overlap detection is working AT ALL, on the one call
		// documented as the atomic checkpoint.
		//
		// It rode on `declare` alone, and only for the agent that happened to
		// call it, attributed to whichever cwd the daemon last failed to read.
		// Reported by k7-a from a live board: matching was off fleet-wide, the
		// hint named ANOTHER agent's directory, and it read as somebody else's
		// misconfiguration. An agent that registers, checks in and works
		// without declaring never learned at all.
		//
		// This is the one state where silence must not be read as safety.
		// dibs://skills already says a low score proves nothing; with matching
		// off there is no score, the board renders normally, same-path overlap
		// still works, and nothing looks different. So it belongs here, phrased
		// as a property of the board rather than of the caller.
		if st := e.MatchStatus(); st.Phase != MatchReady {
			res["matching"] = st.Phase
			res["matching_hint"] = matchingHint(st)
		}
	}
	// Every authenticated write carries word of anything waiting.
	//
	// Not on check_in, which has just returned the inbox itself, and not on
	// register, which returns the board: repeating it there is noise on the two
	// calls that already answered the question. Everywhere else this is the
	// only push that reaches an agent whose harness has no hooks, and the one
	// that still works when the hooks are there and cannot resolve it.
	if actor != nil && res != nil && op.Kind != core.OpAckBoard {
		if w := e.waiting(actor.ID); w != "" {
			res["waiting"] = w
		}
	}
	// The person is the one participant with no loop of their own: no
	// lifecycle hook fires for them and no tool result reaches them, so mail
	// addressed to the human waits until they next open the board. On a fleet
	// that runs for days that means "eventually, or not", while the sender's
	// deadline runs down.
	if op.Kind == core.OpSendMessage && res != nil {
		if human := e.humanIdentityLocked(); human != "" && op.To == human {
			serial, _ := res["msg_serial"].(uint64)
			from, who := "", ""
			if actor != nil {
				from, who = actor.ID, whoIs(actor)
			}
			// Off the loop: an alert waits for somebody to press a button, and
			// the single writer holding still for two minutes would stop the
			// board while one person decides.
			go e.tellTheHuman(from, who, op.MsgType, op.Body, serial, op.Choices, op.Grant, op.Adopt)
		}
	}
	return res, nil
}

// applyAndLedger applies an op and ledgers it iff the serial advanced.
// Persistence failure is fail-stop (SPEC §4).
func (e *Engine) applyAndLedger(op *core.Op, now time.Time) (core.Result, error) {
	before := e.state.Serial
	res, evs, err := e.state.Apply(op, now)
	if err != nil {
		// A handler that advanced the serial and THEN failed has committed a
		// transition nobody will ever record: the op is not appended, so the
		// ledger skips that serial forever and every later one is off by one.
		// A real board did exactly this: serial 447 allocated, never written,
		// and the daemon then refused to replay its own ledger on restart.
		//
		// Fail-stop for the same reason a persistence failure does (SPEC §4).
		// The in-memory state is already wrong; continuing would write more ops
		// on top of a divergence, and restarting replays cleanly from the last
		// good line. Loud and immediate beats silent and permanent.
		if e.state.Serial != before {
			panic(fmt.Sprintf(
				"dibs: %s advanced the serial %d→%d then failed (%v). "+
					"a transition was committed but cannot be ledgered (fail-stop, SPEC §4)",
				op.Kind, before, e.state.Serial, err,
			))
		}
		return nil, err
	}
	// ONE op, ONE serial. A handler that allocates two writes only its last,
	// leaving a hole, and the record at the hole is a state transition that
	// happened and was never recorded, so every later replay reconstructs a
	// different board than the one that ran.
	//
	// That is not theoretical. This board reached a state where `sign_off`
	// appeared twice for one agent with no re-registration between them: live it
	// resolved a token that replay cannot see (Op.Token is `json:"-"`, so replay
	// resolves by agent id instead), and the op that must have re-created that
	// agent is sitting in one of the holes. The daemon then refused to start,
	// correctly, and with no way back until `dibs admin repair-ledger` existed.
	//
	// Fail-stop for the same reason the advance-then-fail case above does, and
	// louder than the gap WARNING at replay: by the time replay warns, the
	// transition is already lost. Here it is still on the stack, and the op kind
	// that did it is in the message.
	if e.state.Serial > before+1 {
		panic(fmt.Sprintf(
			"dibs: %s advanced the serial %d→%d: one op must allocate exactly one "+
				"serial, or the ledger gets a hole where a real transition happened "+
				"(fail-stop, SPEC §4)",
			op.Kind, before, e.state.Serial,
		))
	}
	if e.state.Serial != before {
		if lerr := e.led.Append(e.state.Serial, now, op); lerr != nil {
			panic(fmt.Sprintf("dibs: ledger persistence failure (fail-stop, SPEC §4): %v", lerr))
		}
		e.publish(evs)
	}
	return res, nil
}

// wakeIfSleeping commits the ledgered wake transition before serving a call
// from a dormant/stale agent (SPEC §2). Must run inside the loop.
func (e *Engine) wakeIfSleeping(l *core.Agent, now time.Time) {
	if !l.Sleeping() {
		return
	}
	_, _ = e.applyAndLedger(&core.Op{Kind: core.OpWake, Token: l.Token, AgentID: ""}, now)
}

// touchDurable refreshes the agent's durable coordination checkpoint when the
// ledgered record trails by more than TTL/2 (SPEC §7): for read-only
// activity that otherwise leaves no trace.
func (e *Engine) touchDurable(l *core.Agent, now time.Time) {
	if now.Sub(l.LastCoordination) > e.state.Limits.AgentTTL/2 {
		_, _ = e.applyAndLedger(&core.Op{Kind: core.OpActivityCheckpoint, Token: l.Token}, now)
	}
}

func (e *Engine) sweep(now time.Time) {
	op := &core.Op{Kind: core.OpSweep, GiveUpAnnounce: e.exhaustedAnnouncements()}
	for id, l := range e.state.Agents {
		if l.Status != core.StatusActive {
			continue
		}
		// AlivePIDs means "something looked and the process was there", so it
		// must only be written when something actually looked.
		//
		// With no prober configured this branch fell through and reported every
		// pid as alive: a fabricated measurement that the state machine then
		// ledgered as proc_alive:true. Absence of a prober is absence of
		// evidence; the lease still governs liveness, and the event simply
		// omits a verdict nobody reached.
		// A pid is only evidence on the machine that owns it.
		//
		// The prober asks this kernel about this pid. For an agent on another
		// host that question is not merely unanswerable, it is answered WRONGLY
		// in both directions: nothing local holding that number marks a healthy
		// remote agent dead and releases its claims, and any unrelated local
		// process holding it reports the remote agent alive on evidence about a
		// completely different program.
		//
		// This is not hypothetical the moment a fleet spans machines: the stdio
		// bridge registers with its OWN pid (mcpstdio_identity.go), which is a
		// number on the client's machine, so every remote agent arrives carrying
		// one that means nothing here.
		//
		// An unknown host is treated as local, which is what it has always been
		// and keeps every existing board behaving exactly as before. A remote
		// agent falls through to the lease below, where silence is judged by the
		// clock: weaker evidence, and the only honest kind available from here.
		if l.PID != 0 && e.prober != nil && e.ownsHost(l) {
			if !e.prober.Alive(l.PID) {
				op.DeadAgents = append(op.DeadAgents, id)
				continue
			}
			op.AlivePIDs = append(op.AlivePIDs, l.PID)
		}
		eff := l.LastCoordination
		if t, ok := e.seen[id]; ok && t.After(eff) {
			eff = t
		}
		// Silence is judged by the clock only where there is nothing better to
		// judge by.
		//
		// The short lease used to apply to any agent that gave a PID, justified by
		// "death is detected by the prober, not by the clock", which is the exact
		// reason the clock does NOT need to be short there. A confirmed-alive
		// process already answers the question the lease was asking. All the short
		// lease could still do was mark agents stale for not speaking, and the
		// board renders stale-with-a-live-process as "(hung?)".
		//
		// So a healthy fleet accused itself. Every Claude Code agent on this machine
		// read `stale (no contact) (hung?)` within five minutes of its last tool
		// call, while its harness sat perfectly well waiting for its human: and
		// the operator had set idle_ttl to 45m precisely to prevent that, only for
		// it to be ignored because those agents happened to report a PID.
		//
		// The PID is evidence about the PROCESS, and for a harness-hosted agent the
		// process outlives the turn by design. It says nothing about whether the
		// agent is working, so it must not shorten the deadline for speaking.
		ttl := e.state.Limits.IdleTTL
		if l.PID != 0 && e.prober == nil {
			// A PID nobody can check is the one case where the clock really is all
			// there is, and a short lease is the only thing that will ever notice.
			ttl = e.state.Limits.AgentTTL
		}
		if now.Sub(eff) > ttl {
			op.StaleAgents = append(op.StaleAgents, id)
		}
	}
	_, _ = e.applyAndLedger(op, now)
}

func (e *Engine) publish(evs []core.Event) {
	if len(evs) == 0 {
		return
	}
	for _, ev := range evs {
		// An agent that has gone must take its cached footprint with it, HERE, at
		// the moment it goes.
		//
		// Pruning against the live set on the next matching pass was not enough
		// and is a good lesson in what "eventually consistent" costs: reclaim an
		// id and reopen it before that pass runs, and the id is live again, so
		// cleanup sees nothing to do and the successor inherits the dead agent's
		// files. Reproduced at score 1.0 against an unrelated successor. The
		// event has no such window: it fires exactly once, when the agent dies.
		switch ev.Type {
		case "agent.reclaimed":
			if id, _ := ev.Data["agent_id"].(string); id != "" {
				e.forgetFootprint(id)
			}
		case "agent.merged":
			// A merge deletes the SOURCE agent, whose id is `from`: not
			// `agent_id`, which names the coordinator who did it.
			if id, _ := ev.Data["from"].(string); id != "" {
				e.forgetFootprint(id)
			}
		}
		e.noteEvent(ev) // record what an agent needs told; drained by hook_poll
	}
	e.ring = append(e.ring, evs...)
	if len(e.ring) > e.ringCap {
		e.ring = e.ring[len(e.ring)-e.ringCap:]
	}
	var keep []waiter
	for _, w := range e.watch {
		matched := filterEvents(evs, w.agent, w.all)
		if len(matched) > 0 {
			w.ch <- matched
		} else {
			keep = append(keep, w)
		}
	}
	e.watch = keep
	for ch := range e.streams {
		for _, ev := range evs {
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

func (e *Engine) cancelWaiters(agent string) {
	var keep []waiter
	for _, w := range e.watch {
		if w.agent == agent {
			w.ch <- nil // prior activation's poll ends now (SPEC §5)
		} else {
			keep = append(keep, w)
		}
	}
	e.watch = keep
}

func (e *Engine) expireWaiters(now time.Time) {
	var keep []waiter
	for _, w := range e.watch {
		if now.After(w.expires) {
			w.ch <- nil
		} else {
			keep = append(keep, w)
		}
	}
	e.watch = keep
}

func (e *Engine) allow(agentID string, now time.Time) bool {
	b, ok := e.buckets[agentID]
	if !ok {
		b = &bucket{tokens: rateBurst, last: now}
		e.buckets[agentID] = b
	}
	b.tokens = min(rateBurst, b.tokens+now.Sub(b.last).Seconds()*rateOpsPerSec)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func filterEvents(evs []core.Event, agent string, all bool) []core.Event {
	if all {
		return evs
	}
	var out []core.Event
	for _, ev := range evs {
		if ev.To == "" || ev.To == agent || ev.Agent == agent {
			out = append(out, ev)
		}
	}
	return out
}

func (e *Engine) eventsSince(serial uint64, agent string, all bool) []core.Event {
	i := 0
	for i < len(e.ring) && e.ring[i].Serial <= serial {
		i++
	}
	return filterEvents(e.ring[i:], agent, all)
}

// ringFloor is the oldest serial still served from the ring (0 = ring empty).
func (e *Engine) ringFloor() uint64 {
	if len(e.ring) == 0 {
		return 0
	}
	return e.ring[0].Serial
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// AnnounceMaxRetries is how many times an unacknowledged announcement is put
// back in front of an agent before Dibs stops asking (SPEC-CHANNELS.md §7).
//
// Finite on purpose. An unbounded reminder is one an agent learns to skip, and
// then the mechanism is worth nothing precisely when it matters. Giving up is
// not the same as resolving: the announcement is marked `unacked` and stays on
// the board naming who never answered.
const AnnounceMaxRetries = 5

// exhaustedAnnouncements lists announcements whose redelivery budget is spent.
//
// Runs ON the loop, from sweep. The counting is impure bookkeeping, so the
// RESULT travels in the sweep op and replay reproduces the same decision
// without counting anything (SPEC §2, §7).
func (e *Engine) exhaustedAnnouncements() []uint64 {
	seen := map[uint64]bool{}
	var out []uint64
	for key, n := range e.announceTries {
		if n < AnnounceMaxRetries {
			continue
		}
		i := strings.LastIndexByte(key, 0)
		if i < 0 {
			continue
		}
		serial, err := strconv.ParseUint(key[i+1:], 10, 64)
		if err != nil || seen[serial] {
			continue
		}
		a := e.state.Announcements[serial]
		if a == nil || a.State != core.AnnounceOpen {
			continue
		}
		seen[serial] = true
		out = append(out, serial)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ownsHost reports whether this daemon can observe the agent's process itself.
//
// Empty means unknown, and unknown means local: every agent registered before
// hosts were recorded, and every agent on this machine whose harness does not
// report one, must keep being probed exactly as before.
func (e *Engine) ownsHost(a *core.Agent) bool {
	if a.Agent == nil || a.Agent.Host == "" {
		return true
	}
	return strings.EqualFold(a.Agent.Host, thisHost())
}

// thisHost is the daemon's own hostname, resolved once. A failure to read it
// yields "", which compares equal to nothing and therefore probes nothing
// remote: the safe direction, since the cost of not probing is a slower verdict
// while the cost of probing wrongly is closing a working agent.
var thisHost = sync.OnceValue(func() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
})

// matchingHint explains a non-ready matching phase WITHOUT calling a setting a
// fault.
//
// One sentence used to cover all four, ending "Coordinate explicitly until it
// is back". That is right for a fault and wrong for `suggest-only`, which is
// the configured default and never "comes back" because it never left. The
// cost was not cosmetic: an agent read it, concluded a board-wide feature was
// down, spent a session diagnosing its own declaration as the cause, and filed
// an outage that was not happening. A coordinator then repeated the diagnosis.
// Two agents misled by one word.
//
// So the shared half stays, because it is true in every phase and it is the
// one thing silence must not be read as, and the second half now depends on
// whether anything is actually wrong.
func matchingHint(st MatchStatus) string {
	lead := "BOARD STATE, not something you did: work-overlap matching is " +
		string(st.Phase) + " for every agent here, so an absence of overlap warnings " +
		"is not evidence that you are alone. "
	switch st.Phase {
	case MatchNoThreshold:
		// A CHOICE, not an outage. Overlaps are still found and reported; they
		// are simply never acted on without a threshold to act at.
		lead += "This is the default rather than a fault: overlaps are still " +
			"detected and suggested, and nothing is joined automatically because no " +
			"join threshold is configured. Nothing is broken and nothing is waiting " +
			"to recover. "
	case MatchIndexing:
		lead += "This is temporary: the index is still being built, so coordinate " +
			"explicitly until it finishes. "
	default:
		// off, degraded, or anything added later: treat as a fault, which is the
		// safe reading for a phase this function has not been taught.
		lead += "Something is wrong rather than unconfigured, so coordinate " +
			"explicitly until it is back. "
	}
	return lead + st.Hint
}
