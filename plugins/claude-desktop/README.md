# Dibs for Claude Desktop

Claude Desktop is **not** Claude Code, and it does not have the same extension
system. This folder ships what Desktop actually supports, and says plainly what
it doesn't.

## What works

Desktop is an MCP host, so every Dibs **tool** works there exactly as it does
anywhere else: register an agent, read the board, set a slot, message peers, claim
resources, await events.

## What does not work: automatic mail delivery

**Claude Desktop has no lifecycle hooks.** Hooks (`SessionStart`, `Stop`, …) are
a Claude Code feature. A plugin may *contain* a `hooks/hooks.json`, but it is
inert in Desktop.

That means the `mcp_tool` wake Dibs uses in Claude Code, a hook calling
`hook_poll` over the connection the model already holds, injecting mail as
`additionalContext`, **has no equivalent in Desktop.**

In Desktop, mail arrives when the agent asks for it (`inbox`, `await_events`),
or when you ask it to check. That is a real limitation, not a bug, and it is not
one we paper over: see [WAKE-MECHANISMS.md](https://github.com/agenxy/dibs/blob/main/WAKE-MECHANISMS.md).

We will **not** close this gap with a shell hook. A CLI that reformats mail into
the harness's continuation protocol is Dibs driving the agent: a harness, not a
service. That is a rule, not a preference: [PHILOSOPHY.md](https://github.com/agenxy/dibs/blob/main/PHILOSOPHY.md).

## Install (either route)

Both need `dibd` running and `dibs` on `PATH`. Dibs is a local service; there
is no bundle-only mode, and shipping a copy of the binary inside the bundle would
just give you a second, divergent one.

**Route A: config file (works today, no packaging step).**
Add to `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS), `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "dibs": { "command": "dibs", "args": ["mcp-stdio"] }
  }
}
```

`mcp-stdio` reads the daemon secret from disk locally, so no token is ever
written into a config file.

**Route B. MCPB bundle.** `manifest.json` here is the bundle manifest. Pack with:

```bash
bunx @anthropic-ai/mcpb pack plugins/claude-desktop
```

Then install the resulting `.mcpb` via Desktop's Settings → Extensions.

## Verified

- `dibs mcp-stdio` answers a real MCP handshake: `initialize` → protocol
  `2025-11-25`, server `dibs`, advertising `resources.subscribe`; `tools/list`
  returns the full tool list. Probed directly over a pipe, not inferred.
- `manifest_version: "0.3"` passes `mcpb pack` schema validation and produces a
  bundle. (`repository` must be an **object**, `{type, url}`, not a string; a
  string fails validation. Pack with `bunx --bun @anthropic-ai/mcpb pack .`)
- End-to-end over the stdio path a Desktop client will use: `register`
  created an agent and advanced the ledger serial; `hook_poll` answered `{}`
  (correct, no mail).

**Operational note:** `dibs mcp-stdio` is only a bridge, `tools/list` is
answered by the **daemon**. If `dibd` is older than the binary you just built,
you will silently get the daemon's older tool set. Restart `dibd` after
building, or tools like `hook_poll` appear missing for no visible reason.

## Measured 2026-09-15 (Claude Desktop 1.52386.6)

- The chat client identifies as `claude-ai/0.1.0`, opens with `initialize`
  **2025-11-25**, declares only the MCP Apps UI extension
  (`extensions.io.modelcontextprotocol/ui`), and sends `tools/list` and
  `resources/list`. It does not send `server/discover`; the 2026-07-28 codec in
  the binary is not used.
- Local agent mode spawns a second bridge per configured server, identifying
  as `local-agent-mode-<server>/1.0.0`, also 2025-11-25, with `roots` and the
  UI extension; it sends `tools/list` only.
- **Do not configure Route A while the Claude Code plugin is installed.** The
  app exposes its own `dibs` server to Code-tab sessions under the plugin's
  name, and the plugin's server disappears from those sessions. The app's
  bridge runs from `/` as the app's child, not the session's, so no sidecar
  names its parent and an agent that registers through it binds `host-<pid>`
  instead of its session UUID and its lifecycle hooks
  resolve to nobody. Measured on this machine: the seat registered from the
  Code tab landed on `local-agent-mode-dibs` with `cwd: /`. Route A is for a
  Desktop that runs no Claude Code plugin.
- **Removing Route A means removing it while the app is not running.** The app
  loads `mcpServers` at launch, keeps it in memory, and writes the whole file
  back whenever any preference changes; an entry deleted from the file while
  the app runs came back on the next such write (measured: removed 2026-09-15
  09:13, present again at the next launch). Use Settings → Developer, or quit
  the app and then edit. `dibs doctor` reports the combination until it is
  gone.

## Still unverified

- Nobody has installed the resulting `.mcpb` through Desktop's own UI and watched
  it load. The bundle is valid; the install path is not yet exercised.
- Desktop's user-editable config supports **stdio only**; remote HTTP MCP servers
  are reachable only through managed (MDM) config. So a Desktop client on machine
  A cannot point straight at a Dibs daemon on machine B: it goes through the
  local `dibs mcp-stdio` bridge, which is where networking is configured anyway.
