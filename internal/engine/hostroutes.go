package engine

import (
	"strings"

	"github.com/agenxy/dibs/internal/core"
)

// Decisions the request path makes about WHICH MACHINE an op speaks for,
// kept together and out of engine.go, which is at its file ceiling.

// foldFor folds Windows separators in a path that came from the machine
// named by host, and leaves another machine's path exactly as it arrived.
//
// EVERY ENTRY POINT ASKS THIS, not just the ones somebody remembered. The
// daemon's own platform says what a separator means HERE; on a unix
// machine a backslash is an ordinary filename character, so a Windows hub
// folding `/work/a\b` turns one file into another. Round forty-six fixed
// the op path and left the lifecycle hooks and the write guard folding
// unconditionally, so a claim on a remote unix agent's `/work/a\b` stayed
// literal while the guard asked about `/work/a/b` and answered `allow` for
// a file somebody held exclusively. Round forty-seven of the pre-release
// review.
func (e *Engine) foldFor(host, p string) string {
	if host != "" && host != e.HostID() {
		return p
	}
	return foldSeparators(p)
}

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

// namesAnotherAgentsPath reports an op whose path belongs to a machine
// other than the caller's, whatever host the CALLER is on.
//
// force_release with a holder quotes the path the board shows, which is
// that holder's spelling on that holder's machine. The fold asked only
// whether the CALLER was remote, so a coordinator on a Windows hub
// releasing a unix agent's `/work/a\b` had it folded into `/work/a/b`
// and released a different claim: ok true, the wrong file freed, the one
// it asked for still held. The tool path and the bridge already leave
// this path alone (round forty-six); this is the third place that has to.
// Round forty-eight of the pre-release review.
func namesAnotherAgentsPath(op *core.Op) bool {
	return op.Kind == core.OpForceRelease && op.To != ""
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
