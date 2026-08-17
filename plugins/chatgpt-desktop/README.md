# Dibs for the ChatGPT desktop app

On **2026-07-09 OpenAI merged the Codex app into the ChatGPT desktop app**. There
is no separate Codex desktop app any more: Chat, Work and Codex are surfaces
inside one application, on every plan.

**This matters for Dibs because the ChatGPT desktop app, Codex CLI and the IDE
extension share one MCP configuration.** Configure Dibs once and all three see
it, so this folder is deliberately thin and defers to
[../codex/README.md](../codex/README.md).

## Install

Exactly the Codex configuration. `~/.codex/config.toml`:

```toml
[mcp_servers.dibs]
command = "/absolute/path/to/dibs"
args = ["mcp-stdio"]
```

`dibs mcp-config` prints this with the real path filled in. Use stdio rather
than a url: the bridge is one process per session and it remembers this agent's
nonce, so a returning session reattaches instead of forking a `-2` sibling. See
[../codex/README.md](../codex/README.md) for the reasoning and for the url form,
which is right only from another machine.

## What carries over from Codex, and what does not

Everything measured for Codex applies: every tool reachable, `protocolVersion
2025-06-18` negotiated, `resources/list` never sent, and the `mcp_2026_07_28`
feature flag that does **not** change the wire.

**Wake is pull-only**, for the same reason: `HookHandlerConfig` offers `command`,
`prompt`, `agent`, and only `command` reaches outward: as a subprocess, which
Dibs does not do. See [PHILOSOPHY.md](https://github.com/agenxy/dibs/blob/main/PHILOSOPHY.md).

## Developer mode is the one thing that differs

ChatGPT has a **developer mode** for building and testing MCP apps
(Settings → Connectors → Advanced; for workspaces, Workspace Settings →
Connected Data). That is the surface where Dibs' MCP Apps board panel
([SPEC-APPS.md](https://github.com/agenxy/dibs/blob/main/SPEC-APPS.md)) would render inside ChatGPT: the panel is
already implemented and spec-correct.

## Not verified

Nothing here has been driven against the ChatGPT desktop app itself. The shared
configuration is documented by OpenAI; the Codex half of it is measured, the
ChatGPT half is not.
