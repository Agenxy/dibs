package engine

import (
	"strings"

	"github.com/agenxy/dibs/internal/core"
)

// Decisions the request path makes about WHICH MACHINE an op speaks for,
// kept together and out of engine.go, which is at its file ceiling.

// opIsRemote reports that an op came from a bridge on another machine,
// against this daemon's own identity. Unknown is local, which is every
// board written before host ids existed.
//
// By the TOKEN's row, not callerRow: that one answers for three op kinds
// (the session-alias rules need it to), and a claim is not among them, so
// asking it here called every remote claim local and folded it. The first
// version of this fix was therefore no fix at all, and its own test said
// so. Round forty-six of the pre-release review.
func (e *Engine) opIsRemote(op *core.Op) bool {
	host := ""
	switch {
	case op.Agent != nil && op.Agent.HostID != "":
		host = op.Agent.HostID
	case op.Token != "":
		if l := e.state.AgentByToken(op.Token); l != nil && l.Agent != nil {
			host = l.Agent.HostID
		}
	}
	return host != "" && host != e.HostID()
}

// refuseAmbiguousRelease refuses a force_release that names no holder
// when more than one agent holds that exact path, and says who they are.
//
// An absolute path names a different file on each machine, so two agents
// holding /workspace/repo/file.go on two computers is not a collision:
// both claims are real. The fold released the first one it found, so a
// coordinator unsticking one machine could take the protection off the
// other and leave the one it meant in place.
//
// Refused HERE rather than in the fold: an error from Apply would be
// applied to a ledger written before the selector existed, and an op that
// was accepted when it was written must not be refused on replay. Nothing
// is ledgered by a refusal here. Round forty-four of the pre-release
// review.
func (e *Engine) refuseAmbiguousRelease(op *core.Op) error {
	if op.Kind != core.OpForceRelease || op.To != "" {
		return nil
	}
	// Read straight off the state: exec already runs on the writer loop,
	// and e.query from here sends on the channel the loop is reading,
	// which is a deadlock and not an error (AGENTS.md).
	holders := holdersOfPath(e.state, op.Path)
	if len(holders) <= 1 {
		return nil
	}
	return &core.Error{
		Code: "E_AMBIGUOUS_CLAIM",
		Msg:  "more than one agent holds " + op.Path + ": " + strings.Join(holders, ", "),
		Hint: "name the one you mean with `agent`: an absolute path is a " +
			"different file on each machine, so these are separate claims " +
			"and releasing the wrong one leaves the resource unprotected",
	}
}
