# Lanes for the ChatGPT desktop app

On **2026-07-09 OpenAI merged the Codex app into the ChatGPT desktop app**. There
is no separate Codex desktop app any more: Chat, Work and Codex are surfaces
inside one application, on every plan.

**This matters for Lanes because the ChatGPT desktop app, Codex CLI and the IDE
extension share one MCP configuration.** Configure Lanes once and all three see
it — so this folder is deliberately thin and defers to
[../codex/README.md](../codex/README.md).

## Install

Exactly the Codex configuration — `~/.codex/config.toml`:

```toml
[mcp_servers.lanes]
url = "http://127.0.0.1:4777/mcp"
http_headers = { "X-Lanes-Local" = "<contents of <data-dir>/local.secret>" }
```

`lanes mcp-config` prints this. A stale secret gives a 401 with no other symptom.

## What carries over from Codex, and what does not

Everything measured for Codex applies: every tool reachable, `protocolVersion
2025-06-18` negotiated, `resources/list` never sent, and the `mcp_2026_07_28`
feature flag that does **not** change the wire.

**Wake is pull-only**, for the same reason: `HookHandlerConfig` offers `command`,
`prompt`, `agent`, and only `command` reaches outward — as a subprocess, which
Lanes does not do. See [PHILOSOPHY.md](https://github.com/agenxy/lanes/blob/main/PHILOSOPHY.md).

## Developer mode is the one thing that differs

ChatGPT has a **developer mode** for building and testing MCP apps
(Settings → Connectors → Advanced; for workspaces, Workspace Settings →
Connected Data). That is the surface where Lanes' MCP Apps board panel
([SPEC-APPS.md](https://github.com/agenxy/lanes/blob/main/SPEC-APPS.md)) would render inside ChatGPT — the panel is
already implemented and spec-correct.

## Not verified

Nothing here has been driven against the ChatGPT desktop app itself. The shared
configuration is documented by OpenAI; the Codex half of it is measured, the
ChatGPT half is not.
