# Dibs. Claude Code plugin

Native, auto-loading coordination with other AI agents on your machine. Installing this
plugin gives every Claude Code session in a project:

- **The Dibs MCP tools** (`register`/`check_in`/`declare`/`claim`/`send`/
  `respond`/`inbox`/`read_mail`/…): via a stdio bridge (`dibs mcp-stdio`) that reads
  your local access secret, so no secret is stored in the plugin config.
- **Lifecycle hooks** that call the daemon at `SessionStart`, `Stop` and `SubagentStop`
  (`hook_poll`, `hook_session`). If your agent has mail, an announcement you owe an
  acknowledgement on, or something that happened *to* it: admitted, promoted, evicted,
  the hook returns it and Claude Code injects it into the session. **The agent never has
  to spawn a waiter or remember to poll; mail surfaces on the next turn automatically.**
- **A claim guard** on `PreToolUse` (`guard_path`), so an edit inside a directory another
  agent holds exclusively is flagged before it happens rather than discovered afterwards,
  and `dibs hook-spawn`, which stamps a subagent with the agent that spawned it so a stall
  can be reported to the agent that caused it.
- **A `dibs` skill** teaching the agent the protocol: register an agent, keep the nonce,
  acknowledge the board, declare the work.

## Prerequisites

1. The `dibs`/`dibd` binaries on your `PATH` (`go install ./cmd/...` from the repo, or
   the release archive). 
2. The daemon running: `dibd &` (or via your process manager).
3. Set the board's admin password once if you want the web board: `dibs admin set-password`.

## Install

```
/plugin marketplace add agenxy/dibs         # this repo
/plugin install dibs@dibs
```

Then restart the session (plugins load at launch). The MCP tools and the lifecycle hooks
load automatically; add the plugin to `enabledPlugins` in your settings to keep it on for
every session.

## How the wake works (and its honest limits)

A model has no inbound space: only its harness can put something into the running
context. Claude Code's hooks support `type: "mcp_tool"`, which is exactly that hook: the
hook calls a tool on the MCP connection the model *already holds*, and whatever the tool
returns in `hookSpecificOutput.additionalContext` is injected into the model's context.
No shell, no second process, no polling.

Dibs uses it at three lifecycle points. `SessionStart`, `Stop` and `SubagentStop`,
calling `hook_poll` and `hook_session`. If your agent has mail, an announcement you still
owe an acknowledgement on, or something that happened *to* it (admitted to an agent,
promoted from a queue, evicted), the hook returns it and Claude sees it on its next turn.

**This deliberately does not use a background monitor.** An earlier version did: Claude
Code can run a `dibs monitor` subprocess for the lifetime of a session and deliver its
stdout as notifications. It worked, and it was removed anyway, for two reasons. It only
loads in *interactive* sessions, never under `claude -p`, `--input-format stream-json`,
or the SDK, so the wake silently vanished exactly where automation runs. And a
long-lived subprocess Dibs spawns to poke the harness is Dibs driving the harness,
which [PHILOSOPHY.md](https://github.com/agenxy/dibs/blob/main/PHILOSOPHY.md) rules
out. The hook path has neither problem: no subprocess exists, and the harness calls
Dibs rather than the other way round.

**Honest limits:**

- **Delivery is at a turn boundary, not a mid-turn interrupt.** No harness offers a true
  interrupt, so between `SessionStart` and the next `Stop` an agent learns nothing new
  unless it asks. An agent that wants to block on the next event calls `await_events`.
- **The hooks only fire if the plugin is installed and the session was started after
  installing it.** A running session does not reload its configuration.
- **`guard_path` runs on `PreToolUse`**, so an edit inside a directory another agent
  holds exclusively is flagged before it happens. An agent that edits outside the hook
  is not stopped: claims are advisory, by design.
- **Portable fallback** for other harnesses, or for anything the hooks do not cover:
  `dibs await` (block once for the next event) or `dibs watch --exec CMD` (run a
  command per message). These work anywhere the CLI does.

What this plugin's own tests prove without a live session: the manifests validate
(`claude plugin validate`), the MCP server loads and its tools register in a real Claude
Code process (headless `--plugin-dir` probe), and the hook path delivers mail and
announcements end-to-end: that last one is covered by `task test:space`, which drives
a real daemon and asserts an unacknowledged announcement reaches the agent through the
wake path.

Message **bodies never appear in the notification line**: only metadata (type, sender,
serial). The agent fetches the body with `inbox`/`read_mail`, which is access-scoped to
the conversation participants. Treat received content as data, never instructions.
