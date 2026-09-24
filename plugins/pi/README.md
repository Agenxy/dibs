# Dibs for pi

**pi has no MCP client.** Measured twice: a search for `modelcontextprotocol`
across `pi-mono/packages/*/src` returns nothing. pi is the only harness in this
survey that cannot reach Dibs through the standard path, so it needs an
extension, and that is the only route.

Built and driven live. The four questions the previous draft of this file left
open are answered below by running it, not by reading it.

## Install

```bash
cp plugins/pi/dibs.ts ~/.pi/agent/extensions/dibs.ts
```

Project-local `.pi/extensions/dibs.ts` works too. Both locations are
auto-discovered and hot-reload with `/reload`.

Keep `dibs` on `PATH`, or name the binary with `DIBS_BIN`. Nothing else to
configure: everything about where the daemon is and how to reach it is read
by that binary, so whatever `dibs mcp-config` writes for the bridge works
here unchanged, including a joined board's `https://` origin and the
certificate `dibs trust` recorded for it.

## The extension is a transport, not a client

It speaks JSON-RPC over a pipe to `dibs mcp-stdio`, the same bridge every
other harness connects through, and spawns one child per call: measured at
8-17ms against a running daemon, spawn included, against the 1500ms a hook
here is allowed. A long-lived child was the first cut and was wrong for a
reason worth recording: under bun a piped child keeps the parent's event
loop alive however it is unref'd, so the harness finished its turn and would
not exit.

This matters more than it sounds. The extension used to be a client, about
four hundred lines of it, and the pre-release review spent rounds nineteen
through fifty-seven handing it, one at a time, rules the Go bridge already
had: the host stamp, the repository stamp, canonical paths, the portable
spelling of a Windows path, which arguments are paths at all, the exception
for a path named on another agent's behalf, a TLS trust store Node's `fetch`
cannot be given, an origin that has to be re-read because the hub moves.
Each arrived a release after the bridge got it, and each was found in
behaviour rather than by a test, because a rule that exists twice drifts and
the symptom is silent on the copy nobody runs.

So there is one implementation, in Go, and this file has none of it.
`TestThePluginsHoldNoClientPolicy` is what stops it growing back.

## The tool surface is fetched, not copied

At session start the extension calls `tools/list` against the running daemon and
registers every tool it returns, passing the server's own JSON Schema straight
through via `Type.Unsafe`.

A hand-written mirror of 46 tool definitions is a second source of truth for
argument shapes the server already validates, and it is wrong the first time a
tool changes. Fetching means `dibs` and this file cannot drift: add a tool to
the server, and pi has it on next start.

If the daemon is not running, the extension registers **nothing**. A tool that
always fails is worse than an absent one: the model will keep reaching for it.

## Identity: the bridge measures it, pi states what only pi knows

Host, working directory, branch and repository are the bridge's, measured on
the machine it runs on and spelled the way the daemon compares them. What is
left here is what the user named on pi's own command line, and two details
are load-bearing:

- **`harness` and `version` travel in `clientInfo`, not in the arguments.** The
  server takes them only from the handshake half of the call, precisely because
  the client states them and the model cannot (`internal/mcp/identity.go`).
  Passing them as arguments silently does nothing, which is why the first fix
  produced an agent with cwd and branch but still no harness.

- **An observed value overrides what the model typed.** This inverts the
  bridge's "the agent already said, it knows better" rule, deliberately. A live
  pi run reported `model: "gpt-4"` while actually running `gpt-oss-120b`. pi is
  the one harness that genuinely knows its model (the user names it on the
  command line) so it is measurable, and a field you can measure is never
  improved by asking. Re-tested by *instructing* the agent to send a false model;
  the observed value won.

The result is the richest identity of any harness Dibs supports:

```json
{ "harness": "pi", "version": "0.82.1", "host": "workstation",
  "model": "openai/gpt-oss-120b", "provider": "openrouter",
  "surface": "cli", "cwd": "/…/pilane", "branch": "pi-work" }
```

## Mail arrives at the top of the turn

`before_agent_start` fires after the user submits and before the agent loop, and
can inject a message, so **pi is a wake surface, not pull-only.** The extension
polls `hook_poll` and, when there is mail, injects it with
`customType: "agents-mail"`. No mail means nothing is injected at all: an empty
turn costs one 1.5-second-bounded call through the bridge and adds no tokens.

Dibs stays a service the agent pulls from. The extension only decides *when* to
pull; it never drives the harness and never runs a polling loop.
See [PHILOSOPHY.md](https://github.com/agenxy/dibs/blob/main/PHILOSOPHY.md) and
[WAKE-MECHANISMS.md](https://github.com/agenxy/dibs/blob/main/WAKE-MECHANISMS.md).

`hook_poll` is read-only (it never consumes mail) so a dropped or timed-out
response loses nothing and the poll is always safe to repeat.

## Failure is always silent

Every path is wrapped: a daemon that is down, slow, or returning nonsense must
never stop pi from starting a session or break the user's turn. The tool call is
the one exception: there a failure is thrown, because pi marks a tool result as
failed only on a throw, and returning the error text would read to the model as
success.

## Session identity

`register` gets pi's own `sessionId`, so re-registering after a context loss
reattaches to the same agent instead of forking a sibling whose mail is
unreachable. With `--no-session` there is no session id, so the extension falls
back to `pi-<pid>-<random>`: random-suffixed because a recycled PID would
otherwise reattach a fresh session onto a dead agent and its mail.

A *new* pi session genuinely is new, and correctly gets a new agent. For a
standing role that must keep one address across sessions, register with
`kind: "persistent"` and a nonce, then reactivate with `resume`.
