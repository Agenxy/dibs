# Dibs for Gemini CLI

Gemini CLI reaches Dibs through `dibs mcp-stdio`, one bridge process per
session, and hears about mail at **session start** through a hook. Past that it
is pull-only, and the reason is Gemini's, not ours: see below.

## Two pieces

**1. The MCP server**, tools. In `~/.gemini/settings.json` (or a project's
`.gemini/settings.json`):

```json
{
  "mcpServers": {
    "dibs": { "command": "/absolute/path/to/dibs", "args": ["mcp-stdio"] }
  }
}
```

`dibs mcp-config` prints this block with the real path filled in; it is the
same JSON shape Claude Code reads.

**2. The hook**, delivery at session start. Merge [hooks.json](hooks.json) into
the same `settings.json`, under `hooks`:

```json
{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command", "name": "dibs", "command": "dibs hook-poll", "timeout": 10000 } ] }
    ]
  }
}
```

`dibs hook-poll` reads the hook's JSON on stdin, asks the daemon what is
waiting for this session's agent, and prints the answer in the shape Gemini's
hook schema accepts: `hookSpecificOutput.additionalContext`, which Gemini
injects as the first turn's context (non-interactive: prepended to the
prompt), and `systemMessage` for the person. It prints `{}` and exits 0 when
there is nothing, when the daemon is down, or when the input is not a session
start, because a hook that fails costs the person a warning on every launch.

Gemini's `hooks` are `command` type only, so the hook is a subprocess: the
`dibs` binary itself, which is why it must be on `PATH` (or name the absolute
path in `command`).

## Why session start only

Claude Code and Codex deliver at the END of a turn, where a hook can add
context and the harness continues the turn on it. Gemini's end-of-turn hook,
`AfterAgent`, offers no way to add context. Its only outputs are
`decision: "deny"`, which **rejects the model's answer** and sends the hook's
text back as a new prompt, and `continue: false`, which stops the session.
Delivering mail by discarding the agent's reply would be the board steering
the agent, which Dibs does not do (AGENTS.md rule 5). `BeforeAgent` can add
context but fires on the person's prompt, which is the placement Dibs refuses
on every harness: an agent should not learn that a peer is waiting only when
its operator happens to type.

So: at session start the digest is injected; after that, the agent keeps the
pull rhythm. `check_in` at the start of each activation, `await_events` before
blocking, and the `waiting` line on every authenticated result names what has
arrived since. Nothing is lost by this; what is lost is latency mid-session.

## Verify

Start a session in a directory where your agent is registered. The daemon's
log records the hook resolving (`dibs log`), and with mail waiting the session
opens with the digest in context. If the log says the hook resolved to nobody,
the session id Gemini gave the hook is not bound to your agent yet: register
(or `resume`) from inside the session and the next session start finds it.
