package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// A participant that is not on this computer.
//
// Everything the bridge normally stamps onto a call is an observation about
// the machine it is running on: this host's id, this working directory, this
// checkout, this harness's session. That is right for every harness Dibs has
// had, because the harness and the bridge are the same process tree on the
// same machine.
//
// A ChatGPT conversation reaching Dibs through OpenAI's Secure MCP Tunnel is
// not. The tunnel runs `dibs mcp-stdio` on somebody's Mac and relays calls
// from a browser tab that has no working directory, no repository, no process
// and no machine. Stamped with this bridge's observations, that conversation
// arrives on the board claiming to be working in whatever directory the
// tunnel was started from, on the tunnel's host. Measured before writing
// this: a register through a plain bridge recorded
// `cwd: /private/tmp, host: MacMarine` for a client that had neither.
//
// That is this repository's most expensive recurring bug, self-inflicted. A
// path is evidence on ONE computer, and the claim rule, work-overlap matching
// and the wake router all read it as evidence. A browser tab holding a
// directory claim on somebody else's Mac is a false statement the whole
// coordination model is built on top of.
//
// So the operator running the tunnel says so, once, in the tunnel's own
// config, and this bridge then observes nothing and asserts nothing.
const remoteSessionFlag = "--remote-session"

// remoteSession is set for the life of the process by the flag above.
var remoteSession bool

// openAISessionKey is what the ChatGPT client sends to correlate the calls of
// one conversation. Documented as an anonymized conversation id, session
// scoped, which is exactly the role a harness session id plays here.
//
// NOT a credential, and the documentation says so outright: servers "should
// never rely on them for authorization decisions and must tolerate their
// absence". Dibs draws that line already. A session id is evidence that binds
// an agent to the conversation it is speaking from; the NONCE is the thing
// that proves an identity across restarts, and it is the ChatGPT user who
// supplies it on register, exactly as any other agent does. A board that
// accepted a session id as proof is a bug this repository has already had
// once and fixed.
const openAISessionKey = "openai/session"

// parseBridgeArgs reads the flags `dibs mcp-stdio` accepts.
//
// It REFUSES an argument it does not know, like every other verb here. A
// bridge is configured once, in a harness's own config file, by somebody who
// will not see a warning: a typo that is ignored produces a bridge that
// silently behaves as though the flag were absent, which for this flag means
// a false host on the board.
func parseBridgeArgs(args []string) error {
	for _, a := range args {
		switch a {
		case remoteSessionFlag:
			remoteSession = true
		default:
			return fmt.Errorf("dibs mcp-stdio: unknown argument %q\n\n%s", a, bridgeHelp)
		}
	}
	return nil
}

const bridgeHelp = `dibs mcp-stdio: the stdio bridge, for a host with no HTTP MCP client.

  Normally run by a harness on this machine, named in its MCP config.

  --remote-session   the caller is NOT on this computer: relay its calls and
                     observe nothing about this machine. For OpenAI's Secure
                     MCP Tunnel, which runs this command on your Mac and
                     relays a ChatGPT conversation into it. Without it that
                     conversation registers with this machine's host and the
                     tunnel's working directory, neither of which is true of a
                     browser tab, and a directory claim it takes is a claim on
                     somebody else's files.
`

// warnIfThisLooksRemote says so when a caller announces itself as a ChatGPT
// conversation and this bridge was not told.
//
// The flag could have been inferred from that announcement instead. It is
// not, because inferring it would let a caller decide whether Dibs believes
// its own eyes about which machine this is, and because the operator running
// the tunnel is the one who knows. But a misconfiguration whose only symptom
// is a wrong row on somebody else's board deserves more than silence, and
// stderr is the one channel here that is not the protocol.
func warnIfThisLooksRemote(meta map[string]any) {
	if remoteSession || meta == nil {
		return
	}
	if s, ok := meta[openAISessionKey].(string); ok && strings.TrimSpace(s) != "" {
		warnedRemoteOnce.Do(func() {
			fmt.Fprintf(os.Stderr,
				"dibs mcp-stdio: this caller sent %s, so it is a ChatGPT conversation "+
					"relayed from somewhere else, and this bridge is stamping it with THIS "+
					"machine's host and working directory. Add %s where the tunnel starts "+
					"this command.\n", openAISessionKey, remoteSessionFlag)
		})
	}
}

var warnedRemoteOnce sync.Once

// remoteRegisterSession carries the conversation id from the pass that reads
// `_meta` to the pass that fills register's arguments, which run over the same
// message a few lines apart. A field rather than a re-read because the meta
// pass has already normalised it.
var remoteRegisterSession string

// remoteSessionID is the conversation this call belongs to, or "" when the
// client sent none. Absence is tolerated rather than filled in: a made-up id
// would bind the agent to a session that does not exist, and a poll-only
// participant needs no session at all.
func remoteSessionID(meta map[string]any) string {
	s, _ := meta[openAISessionKey].(string)
	return strings.TrimSpace(s)
}
