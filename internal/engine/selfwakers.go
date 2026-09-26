package engine

import (
	"strings"
	"sync"
)

// Who is going to wake themselves, so this daemon does not do it too.
//
// TWO WRITERS, ONE SOCKET, AND THE OPERATOR SAW BOTH. A Claude Code session
// has exactly one message socket and Dibs had two independent things writing
// to it: this daemon, which finds the socket through the harness's
// ~/.claude/sessions sidecar and sends the digest, and the session's own stdio
// bridge, which finds it through CLAUDE_CODE_MESSAGING_SOCKET and sent a fixed
// content-free sentence. Neither knew the other existed. The bridge is
// in-process and skips the sidecar lookup, so it won the race every time: the
// operator's screen showed the useless line first and the real digest second,
// each wrapped in the harness's own peer-message preamble. Three separate
// releases reworded the useless line; none of them noticed there were two.
//
// So a bridge that can reach its own session says so when it subscribes, and
// this daemon stands down for that agent. The direction is not a preference.
// The bridge's route is the RELIABLE one: a self-sent message is accepted where
// a stranger's is held in bypassPermissions mode, and the bridge knows for
// certain whether it has a socket. This daemon's route is best effort with no
// receipt, and it cannot tell a held message from a delivered one. The party
// that can prove it will deliver is the party that should.
//
// Registered for the life of the subscription, exactly like a host bridge
// (AttachHostBridgeWith): the stream is a live connection to this daemon, so
// "is a bridge listening" is a fact this process holds rather than a guess. A
// dropped connection releases the claim and the socket route resumes on the
// next wake.
type selfWakers struct {
	mu sync.Mutex
	// live counts the subscriptions per agent that declared they can reach
	// their own session. A count rather than a flag: a reconnecting bridge
	// overlaps its old stream with its new one, and a flag cleared by the old
	// stream's release would hand the socket route back while a working
	// bridge was still attached.
	live map[string]int
	// sessions is the harness session each agent's self-waking bridge named,
	// for the refusal log. Not used to decide: see SelfWaking.
	sessions map[string]string
}

// AttachSelfWaker records that an agent's own bridge will put wake notices
// into the session it is running in, and returns the release for when that
// subscription ends. A blank agent registers nothing.
func (e *Engine) AttachSelfWaker(agentID, session string) func() {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return func() {}
	}
	sw := &e.selfWakers
	sw.mu.Lock()
	if sw.live == nil {
		sw.live = map[string]int{}
		sw.sessions = map[string]string{}
	}
	sw.live[agentID]++
	if session != "" {
		sw.sessions[agentID] = session
	}
	sw.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			sw.mu.Lock()
			defer sw.mu.Unlock()
			if sw.live[agentID] <= 1 {
				delete(sw.live, agentID)
				delete(sw.sessions, agentID)
				return
			}
			sw.live[agentID]--
		})
	}
}

// SelfWaking reports whether this agent has a live bridge that will deliver
// its own wake notices, and the session that bridge named.
//
// Keyed on the AGENT, not on the session, and that is deliberate. The claim is
// authenticated by the agent's token, and a bridge only ever writes to the
// session it is itself running in, so a self-waking bridge for this agent is
// by construction the session this agent is reachable in. Requiring the two
// session ids to match would hand the socket route back whenever a harness
// published no session id on its listen, which is the case this exists to
// cover.
func (e *Engine) SelfWaking(agentID string) (bool, string) {
	sw := &e.selfWakers
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.live[agentID] > 0, sw.sessions[agentID]
}
