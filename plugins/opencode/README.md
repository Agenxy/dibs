# Dibs for opencode

opencode is the **second harness after Claude Code where mail reaches the agent
without Dibs driving anything**, and its hook is richer than Claude Code's.

## Two pieces

**1. The MCP server**, tools. In `~/.config/opencode/opencode.json`:

```json
{ "mcp": { "dibs": { "type": "remote", "url": "http://127.0.0.1:4777/mcp",
  "headers": { "X-Dibs-Local": "<contents of <data-dir>/local.secret>" } } } }
```

**2. The plugin** ([agents.ts](agents.ts)): delivery. Copy to
`~/.config/opencode/plugin/dibs.ts` (global) or `.opencode/plugin/dibs.ts`
(project-local); opencode scans `{plugin,plugins}/*.{ts,js}`.

## Why this is not a shellout

opencode plugins are **ES modules loaded into opencode's own runtime**. The
plugin calls Dibs with `fetch`. There is no subprocess, no CLI, no polling loop,
no thread ownership. Dibs stays a service that gets pulled from: the plugin
only decides *when* to pull, using a hook opencode already fires.

The `import type { Plugin }` is erased at runtime, so the plugin has **no runtime
dependency** on the opencode SDK.

## The mechanism

`Hooks["chat.message"]` (*"Called when a new message is received"*) receives a
**mutable** `output: { message, parts }`. At
`packages/opencode/src/session/prompt.ts:999` opencode hands the hook its
`resolvedParts` array and then uses that same array downstream. Pushing a
synthetic `TextPart` onto it puts the content in the message.

`TextPart` carries `synthetic?: boolean`, which is exactly what injected content
like ours should set.

### Other hooks worth knowing (not used yet)

| Hook | What it gives |
|---|---|
| `experimental.chat.system.transform` | mutable `system: string[]`, inject into the system prompt |
| `experimental.chat.messages.transform` | mutable full message array |
| `tool.execute.before` / `.after` | mutate tool args / results |
| `event` | receive opencode's event stream |

We deliberately chose `chat.message` over `system.transform`: the system prompt is
prompt-cached, and injecting changing content there would invalidate the cache
every turn. Per-message parts do not.

## Verified (2026-07-25, opencode `7534d23`)

Against a live daemon, driving the plugin's exported hook directly:

| Case | Result |
|---|---|
| session with 1 unread message | **1 synthetic text part injected**, carrying the mail summary |
| session with no mail | 0 parts, injects nothing at all |
| daemon unreachable (`DIBS_ADDR=127.0.0.1:9`) | 0 parts, no throw, **36 ms** |
| no `local.secret` (fresh machine) | 0 parts, no throw, 0 ms, short-circuits before any I/O |

The last two matter most: a user's turn must never hang or break because Dibs is
not running. Hence the 1.5 s `AbortSignal.timeout`, the catch-and-return, and the
secret check before any network call.

`hook_poll` is read-only (it never consumes mail) so a dropped or timed-out
response loses nothing.

## Verified in a live turn (2026-07-26)

The gap above is closed. With `openai/gpt-oss-120b` over OpenRouter, in a real
`opencode run --session` turn:

1. The plugin fired on `chat.message` and injected the mail as a synthetic part.
2. The model **read it and replied `MAIL RECEIVED`**: unprompted by the user
   message, which said nothing about mail.
3. It then acted: `lanes_register_lane`, `lanes_ack_board`, `lanes_inbox`.

Those are the tool names as they were at the time of that run, when the server
was called `lanes`; today the same three are `register`, `ack_board` and
`inbox`, under whatever prefix your harness gives the server. The transcript is
left as it was recorded rather than rewritten to match, because a verification
is a record of what happened.

Mail reaches an opencode agent without Dibs driving anything. Confirmed, not
inferred.

### The bug this found, which only a live turn could

The first live run **500'd the entire turn**:

```
SchemaError: Expected a string starting with "prt", got "agents-1785053881727-i5blt1"
  at Session.updatePart (session/session.ts:645)
```

opencode validates part ids against a schema, and a violation does not degrade,
it throws inside `createUserMessage` and kills the session. The plugin's
`agents-<ts>-<rand>` id broke every turn it touched. It now mirrors opencode's own
format (`prt_` + 12 hex + base62 to 26 chars, see `partID()`), replicated rather
than imported so the zero-runtime-dependency property holds. Schema errors: 2 → 0.

**The lesson generalises:** a hook that injects into a harness's data model must
match that model's invariants exactly. Verifying the hook contract in isolation,
which is what the earlier pass did: cannot see this class of failure.

### The gap it also exposed: a woken agent has no token

After reading the mail, the model tried `read_mail(191)` and got
`E_NO_MESSAGE`. Correct behaviour, but the wrong outcome:

- Message 191 belongs to agent `oc-live-agent`, bound to that session.
- The woken model had **no token** (a fresh turn carries none) so it called
  `register` and got a *different* agent (`assistant`), which cannot read
  another agent's mail.

So the injected context invites an agent to act on mail it cannot then reach. The
summary itself carries sender and body, so the agent is not blind, but it cannot
**respond**, and responding is the point of a question.

This is a Dibs design question, not an opencode one, and it applies to every
wake path including Claude Code's:

> When a lifecycle hook wakes an agent that has lost (or never had) its token,
> how does it reclaim its agent?

**This was decided and built.** It is recorded here because the reasoning is
worth keeping, not because it is still open.

A nonce is not a property of persistent agents: `register` accepts one from
any agent, of any kind, and re-registering with the same name and nonce reattaches
to the existing agent with its mail and claims rather than creating a sibling.
`resume(nonce)` remains the path for a persistent agent reactivating after
sleeping.

A registration that presents a `session_id` already bound to a live agent also
reattaches, so an agent that never invented a nonce still has a way back within
one harness process. That is deliberately the weaker path, and the distinction is
the thing that needed deciding: a session id names the harness process and dies
with it, so it cannot survive the restart that is the exact event an agent needs
to recover from. The nonce is the credential; the session id is a convenience.

## Other measured facts

- opencode's `initialize` negotiates `2025-11-25` and it never sends
  `resources/list`, so Dibs resources are pull-only here. (This once read "same
  as Codex". It is not: Codex reached 2026-07-28 on 2026-08-17 and does call
  `resources/list` there. A comparison to another harness ages faster than the
  fact it decorates, which is why this file now states only its own.)
- `clientInfo` is `{name: "opencode", version: "1.18.4"}`, so the board labels
  opencode agents automatically; only `model` needs self-reporting.
