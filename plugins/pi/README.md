# Lanes for pi

**pi has no MCP client.** Measured twice — a search for `modelcontextprotocol`
across `pi-mono/packages/*/src` returns nothing. pi is the only harness in this
survey that cannot reach Lanes through the standard path, so it needs an
extension, and that is the only route.

Built and driven live. The four questions the previous draft of this file left
open are answered below by running it, not by reading it.

## Install

```bash
cp plugins/pi/lanes.ts ~/.pi/agent/extensions/lanes.ts
```

Project-local `.pi/extensions/lanes.ts` works too. Both locations are
auto-discovered and hot-reload with `/reload`.

Nothing else to configure. The extension finds the daemon at `127.0.0.1:4777`
and authenticates with `~/.lanes/local.secret`. Override with `LANES_ADDR` and
`LANES_DIR`.

## The tool surface is fetched, not copied

At session start the extension calls `tools/list` against the running daemon and
registers every tool it returns, passing the server's own JSON Schema straight
through via `Type.Unsafe`.

This matters more than it looks. A hand-written mirror of 25 tool definitions is
a second source of truth for argument shapes the server already validates, and
it is wrong the first time a tool changes. Fetching means `lanes` and this file
cannot drift: add a tool to the server, and pi has it on next start.

If the daemon is not running, the extension registers **nothing**. A tool that
always fails is worse than an absent one — the model will keep reaching for it.

## Identity is observed, not asked for

The first real pi run registered a lane with a completely empty `agent`, sitting
on a board next to opencode lanes carrying harness, host, cwd and branch. Every
other harness gets that from the `lanes mcp-stdio` bridge, which fills in blank
fields on the way past; pi has no bridge.

So the extension observes it directly, and two details are load-bearing:

- **`harness` and `version` travel in `clientInfo`, not in the arguments.** The
  server takes them only from the handshake half of the call, precisely because
  the client states them and the model cannot (`internal/mcp/identity.go`).
  Passing them as arguments silently does nothing — which is why the first fix
  produced a lane with cwd and branch but still no harness.

- **An observed value overrides what the model typed.** This inverts the
  bridge's "the agent already said, it knows better" rule, deliberately. A live
  pi run reported `model: "gpt-4"` while actually running `gpt-oss-120b`. pi is
  the one harness that genuinely knows its model — the user names it on the
  command line — so it is measurable, and a field you can measure is never
  improved by asking. Re-tested by *instructing* the agent to send a false model;
  the observed value won.

Note this supersedes the `PI_MODEL` / `PI_PROVIDER` environment route the
earlier draft described. Those are read by the stdio bridge, and pi never
launches one — the extension is the only path that runs.

The result is the richest identity of any harness Lanes supports:

```json
{ "harness": "pi", "version": "0.82.1", "host": "workstation",
  "model": "openai/gpt-oss-120b", "provider": "openrouter",
  "surface": "cli", "cwd": "/…/pilane", "branch": "pi-work" }
```

## Mail arrives at the top of the turn

`before_agent_start` fires after the user submits and before the agent loop, and
can inject a message — so **pi is a wake surface, not pull-only.** The extension
polls `hook_poll` and, when there is mail, injects it with
`customType: "lanes-mail"`. No mail means nothing is injected at all: an empty
turn costs one 1.5-second-bounded HTTP call and adds no tokens.

Lanes stays a service the agent pulls from. The extension only decides *when* to
pull; it never drives the harness, spawns a subprocess, or runs a polling loop.
See [PHILOSOPHY.md](https://github.com/agenxy/lanes/blob/main/PHILOSOPHY.md) and
[WAKE-MECHANISMS.md](https://github.com/agenxy/lanes/blob/main/WAKE-MECHANISMS.md).

`hook_poll` is read-only — it never consumes mail — so a dropped or timed-out
response loses nothing and the poll is always safe to repeat.

## Failure is always silent

Every path is wrapped: a daemon that is down, slow, or returning nonsense must
never stop pi from starting a session or break the user's turn. The tool call is
the one exception — there a failure is thrown, because pi marks a tool result as
failed only on a throw, and returning the error text would read to the model as
success.

## Session identity

`register_lane` gets pi's own `sessionId`, so re-registering after a context loss
reattaches to the same lane instead of forking a sibling whose mail is
unreachable. With `--no-session` there is no session id, so the extension falls
back to `pi-<pid>-<random>` — random-suffixed because a recycled PID would
otherwise reattach a fresh agent onto a dead agent's lane and its mail.

A *new* pi session is genuinely a new agent and correctly gets a new lane. For a
standing role that must keep one address across sessions, register with
`kind: "persistent"` and a nonce, then reactivate with `resume_lane`.
