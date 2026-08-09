# Lanes for Claude Desktop

Claude Desktop is **not** Claude Code, and it does not have the same extension
system. This folder ships what Desktop actually supports, and says plainly what
it doesn't.

## What works

Desktop is an MCP host, so every Lanes **tool** works there exactly as it does
anywhere else: register a lane, read the board, set a slot, message peers, claim
resources, await events.

## What does not work: automatic mail delivery

**Claude Desktop has no lifecycle hooks.** Hooks (`SessionStart`, `Stop`, …) are
a Claude Code feature. A plugin may *contain* a `hooks/hooks.json`, but it is
inert in Desktop.

That means the `mcp_tool` wake Lanes uses in Claude Code — a hook calling
`hook_poll` over the connection the model already holds, injecting mail as
`additionalContext` — **has no equivalent in Desktop.**

In Desktop, mail arrives when the agent asks for it (`inbox`, `await_events`),
or when you ask it to check. That is a real limitation, not a bug, and it is not
one we paper over: see [WAKE-MECHANISMS.md](https://github.com/agenxy/lanes/blob/main/WAKE-MECHANISMS.md).

We will **not** close this gap with a shell hook. A CLI that reformats mail into
the harness's continuation protocol is Lanes driving the agent — a harness, not a
service. That is a rule, not a preference: [PHILOSOPHY.md](https://github.com/agenxy/lanes/blob/main/PHILOSOPHY.md).

## Install (either route)

Both need `lanesd` running and `lanes` on `PATH`. Lanes is a local service; there
is no bundle-only mode, and shipping a copy of the binary inside the bundle would
just give you a second, divergent one.

**Route A — config file (works today, no packaging step).**
Add to `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS), `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "lanes": { "command": "lanes", "args": ["mcp-stdio"] }
  }
}
```

`mcp-stdio` reads the daemon secret from disk locally, so no token is ever
written into a config file.

**Route B — MCPB bundle.** `manifest.json` here is the bundle manifest. Pack with:

```bash
bunx @anthropic-ai/mcpb pack plugins/claude-desktop
```

Then install the resulting `.mcpb` via Desktop's Settings → Extensions.

## Verified

- `lanes mcp-stdio` answers a real MCP handshake: `initialize` → protocol
  `2025-11-25`, server `lanes`, advertising `resources.subscribe`; `tools/list`
  returns the full tool list. Probed directly over a pipe, not inferred.
- `manifest_version: "0.3"` passes `mcpb pack` schema validation and produces a
  bundle. (`repository` must be an **object** — `{type, url}` — not a string; a
  string fails validation. Pack with `bunx --bun @anthropic-ai/mcpb pack .`)
- End-to-end over the stdio path a Desktop client will use: `register_lane`
  created a lane and advanced the ledger serial; `hook_poll` answered `{}`
  (correct — no mail). The config entry below is installed on this machine.

**Operational note:** `lanes mcp-stdio` is only a bridge — `tools/list` is
answered by the **daemon**. If `lanesd` is older than the binary you just built,
you will silently get the daemon's older tool set. Restart `lanesd` after
building, or tools like `hook_poll` appear missing for no visible reason.

## Still unverified

- Nobody has installed the resulting `.mcpb` through Desktop's own UI and watched
  it load. The bundle is valid; the install path is not yet exercised.
- Desktop's user-editable config supports **stdio only**; remote HTTP MCP servers
  are reachable only through managed (MDM) config. So a Desktop client on machine
  A cannot point straight at a Lanes daemon on machine B — it goes through the
  local `lanes mcp-stdio` bridge, which is where networking is configured anyway.
