package engine

import (
	"context"
	"os"
	"os/user"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
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
	if e.human.token == "" {
		return ""
	}
	if e.state.AgentByToken(e.human.token) == nil {
		e.human.token, e.human.agent = "", ""
		return ""
	}
	return e.human.agent
}

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
		Nonce:       "human:" + name,
		SessionID:   "human:" + name,
		PID:         os.Getpid(),
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
