# Dibs for Codex

Codex reaches Dibs as a plain MCP server over HTTP: no bridge, no adapter.

## Install

In `~/.codex/config.toml`:

```toml
[mcp_servers.agents]
url = "http://127.0.0.1:4777/mcp"
http_headers = { "X-Dibs-Local" = "<contents of <data-dir>/local.secret>" }
```

`dibs mcp-config` prints this for you. The secret rotates when the data dir is
recreated; a stale value gives a 401 with no other symptom, so re-copy it if
Codex suddenly sees zero tools.

## Verified on a source build (`61a4488`, 2026-07-25)

- Codex sees **every advertised Dibs tool** and calls them. (The count is not
  written here on purpose: it has been wrong twice, and a number in prose drifts
  the moment a tool is added. `tools/list` is the answer.)
- It negotiates **`protocolVersion: 2025-06-18`**: older than any other harness.
  Dibs echoes that exact version back (see `negotiateLegacy` in
  `internal/mcp/mcp.go`); replying with a different one entitles a strict client
  to disconnect.
- `resources/list` is never sent, so **Dibs resources are invisible to Codex**.
  Only tools reach it. Codex does register `list_mcp_resources` /
  `read_mcp_resource` as model-facing tools, so resources are reachable if the
  model chooses to look: pull, never push.

### The 2026 flag does not do what its name suggests

Codex has a real feature flag, `mcp_2026_07_28` (`Feature::Mcp20260728`), settable as
a boolean in `[features]`. **Enabling it does not change the wire.** Measured with
the flag resolved `true` via `codex features list`:

| | |
|---|---|
| negotiated | `2025-06-18` |
| `server/discover` sent | 0 |
| `subscriptions/listen` sent | 0 |

Codex marks it "under development": it gates unfinished work, not a protocol
switch. Do not assume 2026 support from the flag's presence.

## Waking an agent: now possible, as of the `mcp_tool` hook variant

**This section's conclusion has flipped.** It said mail was pull-only here
because `HookHandlerConfig` had no way to reach Dibs, and ended by telling the
next reader to re-check that enum on upgrade, because one new variant would
change the answer. It has:

```rust
#[serde(rename = "mcp_tool")]
McpTool { server: String, tool: String, input: ..., timeout_sec: ..., status_message: ... }
```

That is the same mechanism the Claude Code plugin uses: a hook that calls a tool
over the MCP connection the model already holds. No subprocess, so nothing here
turns Dibs into a harness driver, and the objection below no longer applies.

Until this is wired, Codex mail is pull-only in practice and a wake digest
surfaces to the HUMAN rather than to the agent it is addressed to, which is how
this was noticed: a person watching their own Codex prompt fill up with mail for
`codex-primary`.

The historical reasoning is kept below, because it is why we refused to solve it
with a `command` hook, and that refusal still stands.

## Why we would not use a `command` hook (still true)

Codex has lifecycle hooks and they **do** support `additionalContext` injection,
the same mechanism Dibs uses in Claude Code. But `HookHandlerConfig`
(`codex-rs/config/src/hook_config.rs:149`) has exactly three variants:

| Variant | Reaches Dibs? |
|---|---|
| `command` | yes, but it is a **subprocess**, which Dibs does not do |
| `prompt` | no, empty struct; injects a prompt, calls nothing out |
| `agent` | no, empty struct; spawns an agent, calls nothing out |

There is **no `mcp_tool` and no `http` variant**, so unlike Claude Code there is no
way for a Codex hook to call Dibs over the connection the model already holds.

We will not close this with a `command` hook. A CLI reformatting mail into the
harness's continuation protocol is Dibs driving the agent: a harness, not a
service. See [PHILOSOPHY.md](https://github.com/agenxy/dibs/blob/main/PHILOSOPHY.md).

**So in Codex, mail is pull-only:** `await_events` / `inbox`, at the agent's
choosing. That is the honest floor, and it works today.

Re-check `HookHandlerConfig` when upgrading Codex, one new variant flips this.

### Plugins do not change this, but they are not the whole surface

A Codex **plugin** (`codex-rs/plugin/src/manifest.rs`) declares four resource
types: `skills`, `mcp_servers`, `apps`, `hooks`. Its `hooks` are the same
`HookHandlerConfig` above, so plugins add no new wake path.

The mechanism that *would* work exists, but is out of third-party reach.
`codex-rs/ext/extension-api/` defines an **extension API** with contributors for
`world_state`, `turn_lifecycle`, `turn_input`, `context`, `tool_lifecycle`, `mcp`,
and more. `WorldStateSectionContribution` is described as *"plain model-visible
data rendered by an extension-owned World State section"*, sampled per turn
(`turn_id`, `turn_store`). That is exactly the shape Dibs wants: model-visible,
per-turn, no subprocess, no thread ownership.

**But it is compiled in, not loaded.** `ExtensionRegistryBuilder` holds
`Vec<Arc<dyn Contributor>>` wired by the host at build time (see
`codex-rs/thread-manager-sample/src/main.rs`); the in-tree extensions are crates
under `codex-rs/ext/` (`skills`, `memories`, `connectors`, `web-search`, …).
There is no dynamic loader. Data flows extension → MCP server
(`McpServerRegistration::from_extension`), never MCP server → extension.

So a third party cannot reach it. **The only route is upstream**: a `codex-agents`
ext crate contributing a `WorldStateContributor` that renders unread-mail counts
into per-turn model-visible state. That is a genuine contribution opportunity,
not a Dibs-side feature: recorded here so the option is not rediscovered later.


## Running Codex on a non-OpenAI provider

Everything above is about Codex↔Dibs, and that half works: Codex connects over
streamable HTTP and enumerates every tool into an `mcp__dibs` namespace,
confirmed from a captured request payload.

Driving Codex against **OpenRouter** is a different matter, and the obstacles
are all Codex↔provider: none of them involve Dibs. Recorded here because the
next person will hit them in this order:

1. **`wire_api = "chat"` is gone.** Codex now requires
   `wire_api = "responses"`. OpenRouter does implement `/api/v1/responses`.

2. **Server tools are rejected.** Codex sends `web_search` as a server tool;
   OpenRouter answers `400 Server tool request failed`. `tools.web_search=false`
   did not remove it in testing.

3. **`namespace`-typed tools are rejected too.** Codex groups MCP tools into
   `{type:"namespace", name:"mcp__dibs", tools:[…]}`, an OpenAI Responses-API
   type OpenRouter does not accept. Codex does this **unconditionally**,
   `codex-rs/core/src/tools/spec_plan.rs` has no flag to flatten it.

   Flattening them in a proxy into plain functions named `mcp__dibs__<tool>`
   (Codex's own `MCP_TOOL_NAME_DELIMITER = "__"`) clears the 400, and the model
   then emits exactly the right call:

   ```
   ERROR codex_core::tools::router: error=unsupported call: mcp__dibs__register
   ```

4. **…which Codex itself then rejects.** Its registry resolves tools by
   `ToolName{namespace, name}` and does not split a flat name back apart. Making
   this work means rewriting streamed responses inside Codex's own plumbing.

So: **Codex needs a provider that implements OpenAI's Responses tool types.**
With one, nothing here should require special handling. The Dibs side is
finished and proven; this is a Codex/provider compatibility gap, and it is worth
re-testing whenever either side ships.

### `reasoning effort: none` is also rejected

Independent of the above: Codex defaults to no reasoning, and gpt-oss-120b on
OpenRouter's Responses endpoint answers *"Reasoning is mandatory for this
endpoint and cannot be disabled."* Set `model_reasoning_effort` to `medium`.

## No supervision hooks, deliberately

Dibs ships no hook file for Codex, and that is a decision rather than a gap.

Codex fires hooks as SUBPROCESSES. A hook that shells out to fetch mail makes
Dibs a thing that drives your harness, which
[PHILOSOPHY.md](https://github.com/agenxy/dibs/blob/main/PHILOSOPHY.md) rules
out. Codex's `mcp_tool` hook type, which Claude Code uses to call a tool on the
connection the model already holds, is not available here.

So on this harness mail is pull-only: call `check_in` at the start of an
activation and `await_events` when you are about to block. That is the honest
floor, it needs no configuration, and it works everywhere.

An earlier version of this document described a `hooks/hooks.json` and claimed
it registered Dibs against Codex's lifecycle. It was never functional: six of
its seven entries used the unsupported type, and the seventh was the subprocess
this project refuses to be.

