# Lanes for Codex

Codex reaches Lanes as a plain MCP server over HTTP — no bridge, no adapter.

## Install

In `~/.codex/config.toml`:

```toml
[mcp_servers.lanes]
url = "http://127.0.0.1:4777/mcp"
http_headers = { "X-Lanes-Local" = "<contents of <data-dir>/local.secret>" }
```

`lanes mcp-config` prints this for you. The secret rotates when the data dir is
recreated; a stale value gives a 401 with no other symptom, so re-copy it if
Codex suddenly sees zero tools.

## Verified on a source build (`61a4488`, 2026-07-25)

- Codex sees **every advertised Lanes tool** and calls them. (The count is not
  written here on purpose: it has been wrong twice, and a number in prose drifts
  the moment a tool is added. `tools/list` is the answer.)
- It negotiates **`protocolVersion: 2025-06-18`** — older than any other harness.
  Lanes echoes that exact version back (see `negotiateLegacy` in
  `internal/mcp/mcp.go`); replying with a different one entitles a strict client
  to disconnect.
- `resources/list` is never sent, so **Lanes resources are invisible to Codex**.
  Only tools reach it. Codex does register `list_mcp_resources` /
  `read_mcp_resource` as model-facing tools, so resources are reachable if the
  model chooses to look — pull, never push.

### The 2026 flag does not do what its name suggests

Codex has a real feature flag, `mcp_2026_07_28` (`Feature::Mcp20260728`), settable as
a boolean in `[features]`. **Enabling it does not change the wire.** Measured with
the flag resolved `true` via `codex features list`:

| | |
|---|---|
| negotiated | `2025-06-18` |
| `server/discover` sent | 0 |
| `subscriptions/listen` sent | 0 |

Codex marks it "under development" — it gates unfinished work, not a protocol
switch. Do not assume 2026 support from the flag's presence.

## Waking an agent: not possible here without a subprocess

Codex has lifecycle hooks and they **do** support `additionalContext` injection —
the same mechanism Lanes uses in Claude Code. But `HookHandlerConfig`
(`codex-rs/config/src/hook_config.rs:149`) has exactly three variants:

| Variant | Reaches Lanes? |
|---|---|
| `command` | yes — but it is a **subprocess**, which Lanes does not do |
| `prompt` | no — empty struct; injects a prompt, calls nothing out |
| `agent` | no — empty struct; spawns an agent, calls nothing out |

There is **no `mcp_tool` and no `http` variant**, so unlike Claude Code there is no
way for a Codex hook to call Lanes over the connection the model already holds.

We will not close this with a `command` hook. A CLI reformatting mail into the
harness's continuation protocol is Lanes driving the agent — a harness, not a
service. See [PHILOSOPHY.md](https://github.com/agenxy/lanes/blob/main/PHILOSOPHY.md).

**So in Codex, mail is pull-only:** `await_events` / `inbox`, at the agent's
choosing. That is the honest floor, and it works today.

Re-check `HookHandlerConfig` when upgrading Codex — one new variant flips this.

### Plugins do not change this — but they are not the whole surface

A Codex **plugin** (`codex-rs/plugin/src/manifest.rs`) declares four resource
types: `skills`, `mcp_servers`, `apps`, `hooks`. Its `hooks` are the same
`HookHandlerConfig` above, so plugins add no new wake path.

The mechanism that *would* work exists, but is out of third-party reach.
`codex-rs/ext/extension-api/` defines an **extension API** with contributors for
`world_state`, `turn_lifecycle`, `turn_input`, `context`, `tool_lifecycle`, `mcp`,
and more. `WorldStateSectionContribution` is described as *"plain model-visible
data rendered by an extension-owned World State section"*, sampled per turn
(`turn_id`, `turn_store`). That is exactly the shape Lanes wants: model-visible,
per-turn, no subprocess, no thread ownership.

**But it is compiled in, not loaded.** `ExtensionRegistryBuilder` holds
`Vec<Arc<dyn Contributor>>` wired by the host at build time (see
`codex-rs/thread-manager-sample/src/main.rs`); the in-tree extensions are crates
under `codex-rs/ext/` (`skills`, `memories`, `connectors`, `web-search`, …).
There is no dynamic loader. Data flows extension → MCP server
(`McpServerRegistration::from_extension`), never MCP server → extension.

So a third party cannot reach it. **The only route is upstream**: a `codex-lanes`
ext crate contributing a `WorldStateContributor` that renders unread-mail counts
into per-turn model-visible state. That is a genuine contribution opportunity,
not a Lanes-side feature — recorded here so the option is not rediscovered later.


## Running Codex on a non-OpenAI provider

Everything above is about Codex↔Lanes, and that half works: Codex connects over
streamable HTTP and enumerates every tool into an `mcp__lanes` namespace,
confirmed from a captured request payload.

Driving Codex against **OpenRouter** is a different matter, and the obstacles
are all Codex↔provider — none of them involve Lanes. Recorded here because the
next person will hit them in this order:

1. **`wire_api = "chat"` is gone.** Codex now requires
   `wire_api = "responses"`. OpenRouter does implement `/api/v1/responses`.

2. **Server tools are rejected.** Codex sends `web_search` as a server tool;
   OpenRouter answers `400 Server tool request failed`. `tools.web_search=false`
   did not remove it in testing.

3. **`namespace`-typed tools are rejected too.** Codex groups MCP tools into
   `{type:"namespace", name:"mcp__lanes", tools:[…]}`, an OpenAI Responses-API
   type OpenRouter does not accept. Codex does this **unconditionally** —
   `codex-rs/core/src/tools/spec_plan.rs` has no flag to flatten it.

   Flattening them in a proxy into plain functions named `mcp__lanes__<tool>`
   (Codex's own `MCP_TOOL_NAME_DELIMITER = "__"`) clears the 400, and the model
   then emits exactly the right call:

   ```
   ERROR codex_core::tools::router: error=unsupported call: mcp__lanes__register_lane
   ```

4. **…which Codex itself then rejects.** Its registry resolves tools by
   `ToolName{namespace, name}` and does not split a flat name back apart. Making
   this work means rewriting streamed responses inside Codex's own plumbing.

So: **Codex needs a provider that implements OpenAI's Responses tool types.**
With one, nothing here should require special handling. The Lanes side is
finished and proven; this is a Codex/provider compatibility gap, and it is worth
re-testing whenever either side ships.

### `reasoning effort: none` is also rejected

Independent of the above: Codex defaults to no reasoning, and gpt-oss-120b on
OpenRouter's Responses endpoint answers *"Reasoning is mandatory for this
endpoint and cannot be disabled."* Set `model_reasoning_effort` to `medium`.

## Supervision hooks

`hooks/hooks.json` registers Lanes against Codex's lifecycle events so a spawned
agent reports its own state, and the agent that spawned it can be told when that
state stops changing.

Codex loads hooks from `<plugin>/hooks/hooks.json` — the same layout and shape
as Claude Code, deliberately: Codex's own feature flag calls them "Claude-style
lifecycle hooks". One plugin shape serves both harnesses.

| event | tool | why |
|---|---|---|
| `SessionStart` | `hook_session` | carries `transcript_path`, so supervision reads the agent's own progress instead of discovering the file by asking the process which ones it holds open |
| `PermissionRequest` | `hook_blocked` | "waiting for a human" and "hung" are indistinguishable from outside and need opposite responses; only the harness knows which this is |
| `SubagentStart` / `SubagentStop` | `hook_session` | Codex models nested agents natively |
| `Stop` / `SessionEnd` | `hook_session` | a finished child, distinguished from a dead one |

Hooks cannot report a hang — a wedged harness runs nothing — so they are half of
the answer. `lanes probe --pid N` is the other half, and works with no
cooperation from the child at all. See SPEC-SUPERVISION.md.
