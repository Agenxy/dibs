# Dibs for Gemini CLI

The extension installs the Dibs stdio MCP server, the coordination skill, and a
session-start mail hook. Mail is pull-only during the session.

## Install

Put `dibs` on the PATH that Gemini CLI uses. From a Dibs checkout, run:

```text
gemini extensions install ./plugins/gemini-cli
```

Review the server and hook in Gemini's consent prompt. If you previously set up
Dibs by hand, remove the old `mcpServers.dibs` and Dibs SessionStart hook from
your Gemini settings to avoid loading duplicates. Keep the unrelated entries.

`gemini extensions list` should show `dibs`. In a new Gemini session, use
`/mcp list` to check for the Dibs tools and `/skills list` for the coordination
skill. Gemini owns server and tool approval.

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

Resume a session in which your agent has checked in (`gemini --resume`: the
same session id). The daemon's log records the hook resolving (`dibs log`),
and with mail waiting the session opens with the digest in context.

A brand-new session carries a new id, and its start resolves to nobody by
design: a supplied id that matches nothing is not guessed at, because guessing
is how one agent's mail ended up in another's context. The agent's first
`check_in` in that session binds the new id (the hook announced it, the
directory matches), and that call's `waiting` line carries what a hook would
have; from the next resume onward the session opens bound.
