package engine

import (
	"log/slog"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/notify"
)

// Who an unidentified lifecycle hook is taken to be.
//
// WHEN A HOOK CANNOT SAY WHO IT IS, measured rather than imagined. A hook
// carries a session id when its harness can interpolate one, and Claude Code
// and Codex both do. Gemini CLI's hooks are plain commands with no template
// variables, so it has nothing to pass: matching the single agent working in
// that directory is the only reason a Gemini agent can be woken at all. The
// fallback is a feature before it is anything else.
//
// It is also a guess, and whose guess it should be depends on how somebody
// works. One agent per checkout, and the guess is right every time. Several
// agents in one monorepo, and a new session should probably prove who it is.
// So it is the operator's, and `directory` is the default because it is what
// the shipped harnesses need.
//
// ONE PLACE, and that is the point of this file. The same resolution happens
// for the mail digest, for guard_path and for subagent attribution, and this
// repository's most expensive recurring bug is a rule applied at one call site
// and not its siblings. guard_path is the one that proves it matters: a
// stranger resolved to the claim holder once made the guard report a path as
// unclaimed while its holder held it exclusively.
type unidentifiedPolicy int

const (
	// takeDirectory is the default: the one live agent working there.
	takeDirectory unidentifiedPolicy = iota
	// refuseDirectory resolves to nobody and says nothing to anybody.
	refuseDirectory
	// askTheOperator resolves to nobody and raises a notification.
	askTheOperator
	// askTheCoordinator resolves to nobody and tells the coordinator agent.
	askTheCoordinator
)

func policyNamed(s string) unidentifiedPolicy {
	switch s {
	case "strict":
		return refuseDirectory
	case "ask":
		return askTheOperator
	case "coordinator":
		return askTheCoordinator
	default:
		return takeDirectory
	}
}

// identity holds the policy and the throttle that keeps it from becoming a
// notification storm.
type identity struct {
	mu     sync.RWMutex
	policy unidentifiedPolicy
	// asked remembers which directories have already been raised, because a
	// harness fires lifecycle hooks continuously: an unidentified session in a
	// loop would otherwise notify the operator once per turn boundary, which
	// is the shape of every alert nobody reads.
	asked map[string]time.Time
}

// askAgain is how long before the same directory may raise a second notice.
const askAgain = 30 * time.Minute

// SetUnidentifiedPolicy applies `[identity] unidentified`.
func (e *Engine) SetUnidentifiedPolicy(name string) {
	e.identity.mu.Lock()
	defer e.identity.mu.Unlock()
	e.identity.policy = policyNamed(name)
}

// resolveHook is THE place a lifecycle hook becomes an agent.
//
// A session id that was SUPPLIED and matched nothing is positive evidence this
// is a different session, never a hint to go looking for a neighbour: that
// rule predates the policy and is not subject to it. The policy governs only
// the case the id is absent, which is the case the fallback was built for.
func (e *Engine) resolveHook(sessionID, cwd, host string) *core.Agent {
	if e.state == nil {
		return nil
	}
	if l := e.state.AgentForHookBySessionOn(sessionID, host); l != nil {
		return l
	}
	if sessionID != "" {
		return nil
	}
	e.identity.mu.RLock()
	p := e.identity.policy
	e.identity.mu.RUnlock()
	if p == takeDirectory {
		return e.state.AgentForHookByDirectoryOn(cwd, host)
	}
	// WHO IT WOULD HAVE BEEN, for the notice only. Resolving it and then not
	// using it is deliberate: a notice that cannot name the agent it is about
	// asks the operator to go and work out what happened, which is the kind of
	// alert that trains people to dismiss them.
	would := e.state.AgentForHookByDirectoryOn(cwd, host)
	e.declined(p, cwd, host, would)
	return nil
}

// declined says what the policy refused to guess, to whoever the policy names.
//
// RUNS ON THE WRITER LOOP, because resolveHook does. That decides the shape of
// both branches. Pushing a notice is engine state and is therefore safe here
// and only here. Raising a notification is I/O against another process, so it
// goes to a goroutine: a loop that blocks on a banner is a board that stops
// answering because nobody dismissed a dialog.
func (e *Engine) declined(p unidentifiedPolicy, cwd, host string, would *core.Agent) {
	who := "an agent"
	if would != nil {
		who = would.ID
	}
	slog.Debug("a hook named no session and the policy declines to guess",
		"cwd", cwd, "host", host, "would_have_been", who, "policy", int(p))
	if p == refuseDirectory || !e.askOnce(cwd) {
		return
	}
	body := "A session in " + cwd + " asked Dibs for its mail and named no " +
		"session id, and [identity] unidentified says not to guess. It would " +
		"have been " + who + ": bind it with bind_session, or let that session " +
		"register."
	if p == askTheCoordinator {
		// The live one first, then a dormant one, for coordinatorOrHuman's
		// reason: a dormant standing agent may still wake, and a notice filed
		// correctly into a mailbox nobody opens is this path's failure mode
		// with an extra step.
		live, dormant := e.coordinatorsByLiveness()
		to := live
		if to == "" {
			to = dormant
		}
		if to == "" {
			slog.Debug("no coordinator to tell about an unidentified session", "cwd", cwd)
			return
		}
		e.pushNoticeFor(to, body, 0, 0, time.Now())
		return
	}
	// BANNER, NOT A QUESTION, and off the loop. Nothing is blocked on the
	// answer: the mail stays on the board and is delivered the moment the
	// session identifies itself, so this is news rather than a decision
	// somebody is waiting on.
	go func() {
		if err := notify.Banner("Dibs", "unidentified session", body); err != nil {
			slog.Debug("could not raise the unidentified-session notice", "err", err)
		}
	}()
}

// askOnce throttles per directory. A harness fires hooks continuously and an
// unidentified one would otherwise notify once per turn boundary.
func (e *Engine) askOnce(cwd string) bool {
	e.identity.mu.Lock()
	defer e.identity.mu.Unlock()
	if e.identity.asked == nil {
		e.identity.asked = map[string]time.Time{}
	}
	now := time.Now()
	if last, seen := e.identity.asked[cwd]; seen && now.Sub(last) < askAgain {
		return false
	}
	e.identity.asked[cwd] = now
	return true
}
