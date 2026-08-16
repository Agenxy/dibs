package engine

import (
	"context"
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
func (e *Engine) HumanIdentity() string {
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
	e.human.mu.Lock()
	defer e.human.mu.Unlock()
	if e.human.token != "" {
		// Confirm the identity still exists: an admin prune, or a data directory
		// swapped underneath us, would otherwise leave a token that authorises
		// nothing and fails on the next action with a bare bad-token error.
		if l := e.state.AgentByToken(e.human.token); l != nil {
			return e.human.agent, e.human.token, nil
		}
		e.human.token, e.human.agent = "", ""
	}

	name := humanName()
	res, err := e.Do(ctx, &core.Op{
		Kind: core.OpRegister, Name: name,
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
	e.human.token, e.human.agent = tok, id
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
	e.human.mu.Lock()
	defer e.human.mu.Unlock()
	id, ok := e.state.Nonces[humanNonce()]
	if !ok {
		return
	}
	l := e.state.Agents[id]
	if l == nil || l.Status == core.StatusArchived || l.PID == 0 {
		return
	}
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
func (e *Engine) tellTheHuman(from, msgType, body string, serial uint64) {
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
		go e.answerForHuman(from, body, serial)
	case core.MsgQuestion:
		report(notify.Banner(title, "asks you a question", oneLine(body)))
	case core.MsgHandoff:
		report(notify.Banner(title, "hands work to you", oneLine(body)))
	default:
		report(notify.Banner(title, "says", oneLine(body)))
	}
}

// answerForHuman puts a request to the person and records what they said, as an
// ordinary response from their own agent: the sender cannot tell it came from a
// dialog rather than a tool call, which is the point. Everything the human does
// on this board goes through the same ops an agent sends.
func (e *Engine) answerForHuman(from, body string, serial uint64) {
	choice, err := notify.Ask("Dibs · "+from+" requests", oneLine(body), "Deny", "Later", "Approve")
	if err != nil || choice == "" || choice == "Later" {
		// Dismissed or deferred is not an answer, and inventing one would be
		// answering on their behalf. The request stays open on the board.
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, token, err := e.HumanAgent(ctx)
	if err != nil {
		return
	}
	disposition := "deny"
	if choice == "Approve" {
		disposition = "approve"
	}
	_, _ = e.Do(ctx, &core.Op{
		Kind: core.OpRespond, Token: token, MsgSerial: serial,
		Disposition: disposition,
		Body:        "answered from the desktop notification",
	})
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
func report(err error) {
	if err != nil {
		slog.Warn("could not notify the human; the message is still on the board", "err", err)
	}
}
