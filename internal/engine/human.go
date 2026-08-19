package engine

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/notify"
)

// The human as a participant.
//
// Until now the board was a window: the human could watch the fleet and could
// not speak to it. Every affordance: join an agent, post, answer a question,
// existed for agents only, and the web board's own test asserted "the operator
// view offers no actions". That is a coherent design for a monitoring tool and
// the wrong one for a coordination service, because the human is the one
// participant who always has context the agents lack.
//
// THE DESIGN CHOICE, and it is the whole of this file: the human is not a new
// kind of actor with a parallel permission surface. **The human gets an AGENT
// identity**, a participant, exactly like any other, and every action goes
// through the same tools with the same token. Nothing in internal/core learns
// that humans exist.
//
// They get no LANE of their own. An agent is a space of work; the human joins
// the ones agents open, reads them, and speaks in them. A space with one
// person in it coordinates nobody.
//
// That is worth stating because the alternative is tempting and much worse: a
// set of admin-only write endpoints that mutate state directly would be a second
// authorization path into the state machine, unledgered by construction unless
// each one remembered to ledger, and invisible to `dibs verify`. Routing the
// human through an ordinary agent means their messages are ordinary messages,
// their memberships replay like anyone's, and an agent cannot tell, or need to
// tell: whether it is talking to a person.
//
// AUTHENTICATION is inherited, not invented. The web board already sits behind
// a session cookie that can only be minted by proving the admin password
// (cmd/dibd/guard.go). Reaching this code means that already happened, so the
// token below is handed out on that basis and never leaves the daemon.
type humanState struct {
	mu    sync.Mutex
	token string
	agent string
}

// HumanIdentity reports the human's agent id WITHOUT creating one.
//
// Reading the board must not make you a participant. An operator who opens the
// page to see what the fleet is doing has joined nothing, declared nothing and
// owes nobody an acknowledgement: registering them on page load would put a
// permanent agent on the roster, count them in the fleet, and subject them to
// liveness sweeps, all for looking.
//
// Empty means "not registered yet", which the board renders as observing.
// HumanIdentity reads the board through the loop, so it is safe to call from an
// HTTP handler or a reporting goroutine. In-loop callers want
// humanIdentityLocked, which must not re-enter.
func (e *Engine) HumanIdentity() string {
	if e.ops == nil {
		// A zero-value Engine has no loop; query would block forever rather than
		// fail, which is the trap AGENTS.md warns about.
		return e.humanIdentityLocked()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := e.query(ctx, func() core.Result {
		return core.Result{"id": e.humanIdentityLocked()}
	})
	if err != nil {
		return ""
	}
	id, _ := res["id"].(string)
	return id
}

// humanIdentityLocked is the same answer for a caller already inside the loop.
//
// The split exists because HumanIdentity is called from BOTH sides: an HTTP
// handler (off the loop, racing the writer) and the send path inside Do (on the
// loop, where re-entering would deadlock). One function could not be correct for
// both, and it was the off-loop callers that were quietly wrong.
func (e *Engine) humanIdentityLocked() string {
	e.human.mu.Lock()
	defer e.human.mu.Unlock()
	if e.human.token != "" {
		if e.state.AgentByToken(e.human.token) != nil {
			return e.human.agent
		}
		e.human.token, e.human.agent = "", ""
	}
	// The token is this daemon RUN's; the identity is the board's.
	//
	// Holding only the token meant the human stopped being the human at every
	// restart, until they unlocked again. Everything keyed off this then went
	// quiet: the board stopped marking their row, and mail addressed to them
	// stopped raising a notification, which is the one path that exists because
	// the person is not in a loop to notice its absence.
	//
	// The registration itself is durable: it is in the ledger, minted with a
	// known nonce. Recovering the id from that is reading what is already
	// there, not registering anybody, so the rule above still holds: opening
	// the board makes nobody a participant.
	if id, ok := e.state.Nonces[humanNonce()]; ok {
		if l := e.state.Agents[id]; l != nil && l.Status != core.StatusArchived {
			return id
		}
	}
	return ""
}

// humanNonce is the recovery credential the human's agent is registered with.
// One place, because HumanAgent writes it and HumanIdentity reads it, and a
// second spelling would make the human two different people.
func humanNonce() string { return "human:" + humanName() }

// HumanAgent returns the human's AGENT id and token, creating it on first use.
//
// Called only from an ACTION: joining an agent, posting, sending a message. The
// identity comes into being the moment the operator first does something, and
// not before.
//
// An agent, not an agent. In SPEC-CHANNELS.md's vocabulary an agent is a
// participant, an identity, a mailbox, a token, and an agent is a space of
// work. The human needs the former and emphatically not the latter: they
// monitor and join the spaces agents create, and a space of their own would
// be a room with one person in it.
//
// (The Go type is still `core.Agent` because the ledger's wire name for a
// participant is frozen as `agent`. That collision is documented at the top of
// internal/core/space.go, and it is easy to fall into: this function was
// called HumanAgent until a reader pointed out it reads as "the human gets a
// space", which is the opposite of what it does.)
//
// PERSISTENT, and registered with a stable nonce, so the same person reattaches
// to the same identity across daemon restarts rather than accumulating a
// graveyard of `ada-2`, `ada-3`. Their mail and their agent memberships are the
// things that must survive, and both hang off that identity.
func (e *Engine) HumanAgent(ctx context.Context) (agent, token string, err error) {
	// NEVER hold e.human.mu across a trip through the writer loop.
	//
	// THE DEADLOCK THIS AVOIDS. This took the mutex and held it, with a defer,
	// while calling e.query and e.Do. The loop is a single goroutine, and the
	// requests it serves take the same mutex: humanIdentityLocked, mayAdopt,
	// and ordinary board rendering all do. So: this locks human.mu, the writer
	// picks up a board request, that request blocks on human.mu, and this
	// blocks waiting for the writer that is now stuck behind it. The one
	// receiver of e.ops is gone and every agent on the board hangs, not just
	// the caller.
	//
	// Snapshot, release, then talk to the loop. Two callers arriving together
	// can both reach the registration below, and that is harmless: it carries
	// the same name and the same nonce, so the nonce path returns the same
	// identity to both and the second write stores what the first already did.
	// A duplicated registration is a far better failure than a frozen daemon.
	//
	// Found by a pre-release review. The race probe beside this cannot see it:
	// its competing traffic is registrations, which never touch human.mu.
	e.human.mu.Lock()
	cachedID, cachedTok := e.human.agent, e.human.token
	e.human.mu.Unlock()

	if tok := cachedTok; tok != "" {
		// Confirm the identity still exists: an admin prune, or a data directory
		// swapped underneath us, would otherwise leave a token that authorises
		// nothing and fails on the next action with a bare bad-token error.
		//
		// Read THROUGH the loop. This called state.AgentByToken directly from
		// whichever goroutine wanted the human, and that method iterates
		// State.Agents while the writer mutates it on every registration.
		// e.human.mu protects the cached fields beside it and nothing in core's
		// maps, so it was a plain data race: a targeted -race probe reported
		// several and ended in `fatal error: concurrent map iteration and map
		// write`, which takes the daemon down.
		//
		// RepairHumanProcess and HumanTouch had already been corrected for
		// exactly this, by an earlier review, and this one was missed both
		// times. The ordinary suite stays green because nothing in it calls
		// HumanAgent concurrently with registrations.
		alive := false
		if res, qerr := e.query(ctx, func() core.Result {
			return core.Result{"alive": e.state.AgentByToken(tok) != nil}
		}); qerr == nil {
			alive, _ = res["alive"].(bool)
		}
		if alive {
			return cachedID, cachedTok, nil
		}
		// Clear only what we actually observed to be dead: another caller may
		// have replaced it while we were off the mutex.
		e.human.mu.Lock()
		if e.human.token == cachedTok {
			e.human.token, e.human.agent = "", ""
		}
		e.human.mu.Unlock()
	}

	name := humanName()
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: name,
		// The one registration allowed to be this identity. See core.Op.HumanMint.
		HumanMint:   true,
		Description: "the human at the board",
		AgentKind:   core.KindPersistent,
		Nonce:       humanNonce(),
		SessionID:   humanNonce(),
		// No pid, deliberately. A person is not a process, and this used to be
		// the DAEMON's pid: after a restart the sweep probed a dead process and
		// reported the operator as `process_exited`. Their liveness is silence,
		// governed by idle_ttl like any agent that registers without one.
		NoProcess: true,
		Agent: &core.AgentInfo{
			Harness: "dibs web", Surface: "web", Host: hostname(),
		},
	})
	if err != nil {
		return "", "", err
	}
	tok, _ := res["token"].(string)
	id, _ := res["agent_id"].(string)
	if tok == "" || id == "" {
		return "", "", core.ErrBadToken
	}
	// The awareness gate applies to the human exactly as it does to an agent
	// (SPEC §6): you may not declare work before acknowledging what others are
	// doing. Doing it here rather than making the UI do it keeps the rule in one
	// place, and the human HAS just looked at the board, which is the point of
	// the gate.
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		return "", "", err
	}
	e.human.mu.Lock()
	e.human.token, e.human.agent = tok, id
	e.human.mu.Unlock()
	return id, tok, nil
}

// RepairHumanProcess clears a pid recorded against the human by older code.
//
// The fix above stops the daemon writing its own pid into a person's row, but
// it only fixes rows written after it, and the wrong pid is already in the
// ledger of every board that ran the old build. Nothing heals it on its own:
// the human's registration is written on an ACTION, so a person who reads their
// board and closes it is told `process gone` about themselves forever, which is
// both false and the exact opposite of the honesty the board is for.
//
// Gated on the row EXISTING and holding a pid, so it repairs and never
// registers: a board whose operator has never acted still has no human on it,
// which is the rule HumanIdentity's comment sets out. Gated on the pid so it is
// a one-time correction rather than an op every daemon start, which would grow
// the ledger by a record a day to say nothing.
func (e *Engine) RepairHumanProcess(ctx context.Context) {
	// The decision is read THROUGH the loop; only the answer comes back out.
	//
	// This read Nonces, Agents and a token directly, from a startup goroutine,
	// while the writer was already serving. e.human.mu protects the cached human
	// fields and nothing in core's maps, so that was a plain data race and a
	// candidate for a concurrent map read-and-write panic. Found by an
	// independent review before release.
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := e.query(ctx2, func() core.Result {
		id, ok := e.state.Nonces[humanNonce()]
		if !ok {
			return core.Result{}
		}
		l := e.state.Agents[id]
		if l == nil || l.Status == core.StatusArchived || l.PID == 0 {
			return core.Result{}
		}
		return core.Result{"id": id, "token": l.Token}
	})
	if err != nil {
		return
	}
	id, _ := res["id"].(string)
	token, _ := res["token"].(string)
	if id == "" || token == "" {
		return
	}
	l := struct{ Token string }{token}
	// An update, not a registration. Spelling a correction as a register looks
	// natural and does nothing: register short-circuits a same-nonce retry
	// inside one TTL and returns the original result without applying the op, so
	// the repair silently succeeded and changed nothing on exactly the boards
	// where the row was fresh. Update revises a participant's recorded facts,
	// which is what this is.
	//
	// The description is restated because update takes what it is given: it is
	// a full statement of what the participant says about itself, and omitting
	// it clears it.
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpUpdate, Token: l.Token,
		Description: "the human at the board",
		NoProcess:   true,
	}); err != nil {
		// Not fatal and not worth failing a boot over: the board is wrong in one
		// row until the operator next acts, which is where it was before.
		slog.Warn("could not clear the stale pid on the human's row", "agent", id, "err", err)
	}
}

// HumanTouch refreshes the human's liveness without writing anything.
//
// A person reading the board is present in every sense that matters, but they
// produce no ops while reading, so without this the sweep would eventually mark
// them stale and release any agent they own out from under them mid-read.
func (e *Engine) HumanTouch(ctx context.Context) {
	e.human.mu.Lock()
	tok := e.human.token
	e.human.mu.Unlock()
	if tok == "" {
		return
	}
	_, _ = e.query(ctx, func() core.Result {
		if l := e.state.AgentByToken(tok); l != nil {
			e.seen[l.ID] = time.Now()
		}
		return core.Result{}
	})
}

// humanName is what the fleet sees. The OS username, because an agent reading
// "ada asked you to stop" learns something, and "human-1" does not.
func humanName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return sanitiseName(u.Username)
	}
	if n := os.Getenv("USER"); n != "" {
		return sanitiseName(n)
	}
	return "human"
}

func sanitiseName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "human"
	}
	return string(out)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// mayAdopt reports whether this caller may take over an abandoned mailbox.
//
// Three ways, and no fourth. The human proven present at this machine, because
// the mail is theirs and Touch ID is the one identity here that cannot be
// asserted. An admin, who can already read every mailbox, so this grants
// nothing new. A coordinator, whose whole job is clearing debris nobody else
// can.
//
// Not "an agent in the same directory", which is the shape that keeps looking
// reasonable and is exactly how AgentForHook once handed a stranger somebody
// else's private mail.
func (e *Engine) mayAdopt(l *core.Agent) bool {
	if l.Role == "admin" || l.Role == "coordinator" {
		return true
	}
	e.human.mu.Lock()
	defer e.human.mu.Unlock()
	// The human identity is minted by a presence check (Touch ID, or the admin
	// password where there is no sensor), so holding it IS the proof.
	return e.human.agent != "" && e.human.agent == l.ID
}

// tellTheHuman raises a desktop notification when a message lands for the
// person, and offers the buttons a `request` is asking for.
//
// The human is the one participant who is not in a loop. Every other agent
// learns about mail from a lifecycle hook or from the result of a call it was
// making anyway; the person learns when they next look at the board, which on a
// fleet that runs for days means "eventually, or not". A request addressed to
// them then sits unanswered while its sender's deadline runs out.
//
// Off the writer loop, deliberately. An alert waits for a human to press a
// button, and the single-writer goroutine holding still for two minutes would
// stop the whole board while one person decides.
//
// Nothing here is content beyond what the sender wrote to this human, which
// they are entitled to read: this is their own mailbox arriving by another
// route, not a broadcast of somebody else's traffic.
func (e *Engine) tellTheHuman(from, who, msgType, body string, serial uint64, choices []string, grant, adopt string) {
	if !notify.Available() {
		return
	}
	// Logged because this path has no other evidence it ran. It happens on a
	// goroutine, off the writer loop, and its whole output is a banner on
	// somebody's screen: if it silently does nothing there is nothing to read.
	slog.Info("notifying the human", "from", from, "type", msgType, "msg", serial)
	title := "Dibs · " + from
	switch msgType {
	case core.MsgRequest:
		// A request is literally "approve or deny", so ask it that way. An
		// alert rather than a banner because a banner cannot carry buttons
		// without an application bundle, which Dibs does not yet ship.
		go e.approveForHuman(from, who, body, serial, grant, adopt)
	case core.MsgQuestion:
		// Answerable, not merely announced. A question that arrives as a banner
		// is a notification that the board has something on it, which is what
		// the board already was: the person still has to go and open it, and the
		// asking agent waits out its deadline while they decide whether to.
		go e.answerForHuman(from, who, body, serial, choices)
	case core.MsgHandoff:
		e.report(notify.Banner(title, "hands work to you", oneLine(body)))
	default:
		e.report(notify.Banner(title, "says", oneLine(body)))
	}
}

// approveForHuman puts a request to the person and records what they said, as an
// ordinary response from their own agent: the sender cannot tell it came from a
// dialog rather than a tool call, which is the point. Everything the human does
// on this board goes through the same ops an agent sends.
func (e *Engine) approveForHuman(from, who, body string, serial uint64, grant, adopt string) {
	// The TITLE is the daemon's sentence, not the sender's.
	//
	// It is the only line on the notification that states the EFFECT of pressing
	// Approve, so it must come from the typed field rather than from the prose
	// beside it. An agent writes the body; if the body were the only thing the
	// person read, a request that says "just need to check something" could
	// carry grant: coordinator and be approved by somebody who never saw the
	// word. The body is still shown, as the reason, underneath.
	//
	// Every effect, not the first one. This was a switch, so a request carrying
	// BOTH a grant and an adoption rendered as "make X coordinator?" and moved
	// a mailbox on the same yes. core.Admit now refuses that combination, and
	// this no longer relies on it: if a second effect ever reaches here, the
	// person reads it rather than approving it blind. A prompt that can only
	// describe one of two things is the wrong place to put the assumption that
	// there is only ever one.
	title := "Dibs · " + from + " requests"
	switch {
	case grant != "" && adopt != "":
		title = "Dibs · make " + from + " " + grant + " AND give it " + adopt + "'s mail?"
	case grant != "":
		title = "Dibs · make " + from + " " + grant + "?"
	case adopt != "":
		title = "Dibs · give " + adopt + "'s mail to " + from + "?"
	}
	choice, err := notify.Ask(title, said(who, body), "Deny", "Later", "Approve")
	if errors.Is(err, notify.ErrCannotNotify) {
		// Nobody saw it. Say so, rather than letting the asker time out against a
		// notification that never appeared.
		e.reportNotifyFailure(err)
		return
	}
	if err != nil || choice == "" || choice == "Later" {
		// Dismissed or deferred is not an answer, and inventing one would be
		// answering on their behalf. The request stays open on the board.
		//
		// Said out loud when the ASK itself came back empty, because that is the
		// case where the operator may never have been shown anything, and until
		// this it was indistinguishable from a deliberate "not now". An agent
		// then waited out its deadline against a question nobody saw.
		if err != nil {
			slog.Warn("the human was asked and nothing came back",
				"from", from, "msg", serial, "err", err)
		}
		return
	}
	disposition := "deny"
	if choice == "Approve" {
		disposition = "approve"
	}
	e.respondAsHuman(serial, disposition, "answered from the desktop notification")
}

// answerForHuman puts a question to the person and records their answer.
//
// Two shapes, because a question has two. When the asker enumerated the answers
// they become the buttons, and answering is one press with nothing to type and
// no window to find. When it did not, the notification offers to open a box,
// and only then does anything take the screen.
//
// That order is the whole design. The alternative is to raise the text box on
// arrival, which is a coordination service deciding that its optional question
// outranks whatever the person was doing: the same reason Ask goes through the
// bundle rather than a modal alert. Nothing here steals focus until the human
// has pressed something asking it to.
func (e *Engine) answerForHuman(from, who, body string, serial uint64, choices []string) {
	title := "Dibs · " + from + " asks"
	line := said(who, body)
	plan := planAnswer(choices)

	pressed, err := notify.Ask(title, line, plan.Buttons...)
	if errors.Is(err, notify.ErrCannotNotify) {
		e.reportNotifyFailure(err)
		return
	}
	if err != nil || pressed == "" || pressed == deferButton {
		// Dismissed or deferred is not an answer, and inventing one would be
		// answering on their behalf. The question stays open on the board.
		return
	}
	if plan.Then == "" {
		e.respondAsHuman(serial, "answer", pressed) // the press WAS the answer
		return
	}

	// Only now, after a press that asked for it, does anything take the screen.
	var answer string
	if plan.Then == thenPick {
		answer, err = notify.Pick(title, line, choices...)
	} else {
		answer, err = notify.Prompt(title, line)
	}
	if err != nil || strings.TrimSpace(answer) == "" {
		// Opening the box and closing it again is still not an answer.
		return
	}
	e.respondAsHuman(serial, "answer", answer)
}

// How an answer is collected once the human has asked to give one.
const (
	thenPick   = "pick"   // a list, because the choices did not fit as buttons
	thenPrompt = "prompt" // a text box, because there were no choices
)

// deferButton is the way out that is offered on every question and means
// nothing: it exists so dismissing is a deliberate press rather than the only
// thing a person can do with a notification they do not want to answer yet.
const deferButton = "Later"

// answerPlan is how a question will be put to the person: what the notification
// carries, and what pressing it opens.
type answerPlan struct {
	Buttons []string
	Then    string // "" when the press itself is the answer
}

// planAnswer decides the shape of the interaction, separately from performing
// it, because performing it means osascript and a person at a keyboard and
// neither is available to a test. The decision is the part with a rule in it.
//
// The rule: NOTHING opens without a press first. A question is by definition
// something its asker can wait for, and a coordination service that raises a
// text box over whatever the person was doing has decided otherwise on their
// behalf. It is the same reason Ask goes through the application bundle instead
// of a modal alert.
//
// Three buttons is what a notification carries, so up to three choices ARE the
// buttons and answering is one press with nothing to type and no window to
// find. A fourth cannot be, and rather than silently dropping it the
// notification offers the list.
func planAnswer(choices []string) answerPlan {
	if n := len(choices); n > 0 && n <= 3 {
		return answerPlan{Buttons: choices}
	}
	if len(choices) > 0 {
		return answerPlan{Buttons: []string{deferButton, "Pick one…"}, Then: thenPick}
	}
	// "Write answer…", not "Answer".
	//
	// A button labelled Answer on a notification promises a field that is not
	// there: you press it expecting to type, and the notification vanishes while
	// a box opens somewhere else. Reported exactly that way, twice: "answer is
	// misleading as I would assume I would put my answer somewhere". The verb
	// now says what the press DOES, and the ellipsis keeps the platform's own
	// promise that something further opens.
	return answerPlan{Buttons: []string{deferButton, "Write answer…"}, Then: thenPrompt}
}

// respondAsHuman records the person's answer as an ordinary response from their
// own agent. One place, because every path that answers on their behalf has to
// mint the same identity and go through the same op.
func (e *Engine) respondAsHuman(serial uint64, disposition, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, token, err := e.HumanAgent(ctx)
	if err != nil {
		slog.Warn("could not answer for the human; the message stays open", "msg", serial, "err", err)
		return
	}
	if _, err := e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: token, MsgSerial: serial,
		Disposition: disposition, Body: body,
	}); err != nil {
		slog.Warn("the human answered but the response did not land", "msg", serial, "err", err)
	}
}

// oneLine keeps a notification readable. A banner truncates anyway, and a
// multi-paragraph handoff rendered into one is unreadable rather than informative.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 180
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// report says when a notification could not be delivered. Silence here is the
// failure mode this whole path exists to remove, so it must not fail silently
// itself.
// A method now, so the failure reaches the coordinator and not only the log.
// A log line is a report to nobody: the operator is not tailing it, which is
// the same reason this whole notification path exists.
func (e *Engine) report(err error) {
	if err != nil {
		slog.Warn("could not notify the human; the message is still on the board", "err", err)
		e.reportNotifyFailure(err)
	}
}

// mayApproveGrant refuses a role grant approved by anybody but the person.
//
// The send-time check is not enough, and the gap is not obvious. The engine
// verifies that a request carrying `grant` is ADDRESSED to the human when it is
// sent. Adoption then rewrites the `to` of every message in a mailbox, and a
// coordinator is allowed to adopt. So:
//
//  1. an agent asks the human for coordinator;
//  2. the human's row goes dormant, which needs no help: they are a person, and
//     silence is their whole liveness model;
//  3. an existing coordinator adopts that mailbox, which it is permitted to do;
//  4. the pending request is now addressed to the coordinator;
//  5. it approves, and promotes the asker with no human anywhere in the story.
//
// Found by an independent review before release. The rule this restores is the
// one the feature was built on: a role is a human's to give, and "addressed to
// the human" has to be true at the moment somebody says yes, not merely at the
// moment somebody asked.
//
// Refused at ingress, so nothing is ledgered and replay never sees it.
func (e *Engine) mayApproveGrant(actor *core.Agent, op *core.Op) error {
	if op.Disposition != "approve" {
		return nil // denying a grant needs no authority; it grants nothing
	}
	m := e.state.Messages[op.MsgSerial]
	if m == nil {
		return nil
	}
	human := e.humanIdentityLocked()
	if m.Grant != "" && (human == "" || actor.ID != human) {
		return core.ErrGrantNeedsHuman
	}
	// The same rule as adopt_agent, at the OTHER door.
	//
	// guardHumanMailbox covered the direct call and nothing else, so an agent
	// could send a request carrying `adopt: <the human>`, a coordinator could
	// approve it in good faith, and the operator's whole mailbox moved to the
	// asker. Found by an independent reviewer constructing exactly that.
	//
	// The lesson is the shape rather than the instance: an effect with two entry
	// points needs its rule at the effect, and this is the second escalation in
	// one release from guarding a door instead. Both doors now consult this
	// sentence, and there are only two.
	if m.Adopt != "" && human != "" && m.Adopt == human && actor.ID != human {
		return core.ErrHumanMailboxIsTheirs
	}
	return nil
}

// guardHumanMailbox stops anybody but the person adopting the person's mail.
//
// core/roles.go states the coordinator boundary outright: "It gets no power to
// *read* another agent's mail. Breadth, not intrusion." Adoption moves a
// mailbox, and reading it afterwards is the point, so a coordinator adopting
// the HUMAN's identity walks straight through that sentence and collects every
// private message anybody ever sent the operator, plus any pending request
// addressed to them.
//
// A person's row is dormant most of the time by design, since silence is their
// entire liveness model, so "not active" is not a signal that their mail is
// abandoned. It is the normal state of a human who is not currently typing.
//
// The human may still adopt their own identity, which is what recovery looks
// like for them.
func (e *Engine) guardHumanMailbox(actor *core.Agent, target string) error {
	human := e.humanIdentityLocked()
	if human == "" || target != human || actor.ID == human {
		return nil
	}
	return core.ErrHumanMailboxIsTheirs
}

// whoIs describes the agent behind a request, for a person deciding whether to
// say yes.
//
// "Dibs · make asker coordinator?" is not enough to approve a privilege change
// on, and the operator said so on seeing one: "I don't know who the asker is,
// that's a gap and security risk." A name is whatever the agent called itself,
// chosen in its first seconds and changeable with `update`; on a board where
// three rows are variations of one word it identifies nobody.
//
// So this composes the line from what makes the agent FINDABLE: the id the
// daemon assigned and the board addresses, then where it is working and on what
// machine. A person can match that against a window they have open.
//
// It is still, mostly, the agent's own word. The harness and cwd arrive from
// the bridge but nothing stops an agent asserting them, and this must not imply
// otherwise: the id is the only part the daemon issues, which is exactly why the
// id leads and is never replaced by the display name. Read it as "who this
// claims to be", and treat a request you cannot place as one to deny.
func whoIs(l *core.Agent) string {
	parts := []string{l.ID}
	if l.Name != "" && l.Name != l.ID {
		parts = append(parts, "(displayed as "+l.Name+")")
	}
	if a := l.Agent; a != nil {
		where := a.Project
		if where == "" {
			where = a.CWD
		}
		switch {
		case a.Harness != "" && where != "":
			parts = append(parts, a.Harness+" in "+where)
		case a.Harness != "":
			parts = append(parts, a.Harness)
		case where != "":
			parts = append(parts, where)
		}
		if a.Branch != "" {
			parts = append(parts, "on "+a.Branch)
		}
		if a.Host != "" {
			parts = append(parts, "at "+a.Host)
		}
	}
	return strings.Join(parts, " · ")
}

// said puts WHO above what they wrote.
//
// The identity line is composed by the daemon; the body is the agent's own
// text. Keeping them on separate lines, in that order, means the first thing
// read is the part the sender did not author. A request whose body says
// "routine, just approve" cannot be the first thing a person sees.
func said(who, body string) string {
	line := oneLine(body)
	if who == "" {
		return line
	}
	return who + "\n" + line
}

// humanDeadline is how long a question or request to a PERSON waits before it
// expires.
//
// A day, because that is the honest unit for somebody who answers when they
// next look at their machine rather than on their next turn. Well inside the
// seven days core allows a persistent recipient, so a sender who wants longer
// can still ask for it, and a sender who wants shorter can still say so.
//
// Not forever: a request nobody answers has to end, or the board fills with
// asks whose context is long gone and whose askers have moved on.
const humanDeadline = 24 * time.Hour

// wouldTakeHumanIdentity reports whether a caller's op would land on the
// operator's own agent.
//
// Split from exec so the decision is testable: on a zero-value Engine
// e.query() blocks forever rather than failing, so anything reachable only
// through the wrapper is effectively untested. See AGENTS.md.
//
// Two ways in, and both are closed. The nonce may RESOLVE to the human's row on
// a board where it already exists, and it may simply BE the mint credential on
// a board where it does not: pre-creating the row on a fresh daemon is the same
// takeover a day earlier. The (name, session_id) path needs no guard, because
// it refuses any agent that already holds a nonce and the human always does.
func (e *Engine) wouldTakeHumanIdentity(op *core.Op) bool {
	if op == nil || (op.Kind != core.OpRegister && op.Kind != core.OpResume) {
		return false
	}
	if op.Nonce == "" {
		return false
	}
	if op.Nonce == humanNonce() {
		return true
	}
	if h := e.humanIdentityLocked(); h != "" {
		if id, ok := e.state.Nonces[op.Nonce]; ok && id == h {
			return true
		}
	}
	return false
}
