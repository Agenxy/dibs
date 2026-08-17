# Dibs for Codex

Codex reaches Dibs as a plain MCP server over HTTP: no bridge, no adapter.

## Install

In `~/.codex/config.toml`:

```toml
[mcp_servers.dibs]
command = "/absolute/path/to/dibs"
args = ["mcp-stdio"]
```

`dibs mcp-config` prints this with the real path filled in.

**stdio rather than a url, and the difference is an identity.** The bridge is one
process per session, and it is what remembers this agent's nonce, so a returning
session reattaches to the same agent with its mail instead of forking a `-2`
sibling that cannot read a word of its predecessor's. An HTTP client has no such
process, and nothing else in the stack can hold that credential: the agent's own
context is exactly what ends.

This file used to print the url form, Codex took it, and the cost was invisible
for months. A board carrying nine rows for five roles is what it looks like from
outside.

Use the url form only from ANOTHER machine, where a local bridge is not an
option and a forked identity is the lesser problem:

```toml
[mcp_servers.dibs]
url = "http://127.0.0.1:4777/mcp"
http_headers = { "X-Dibs-Local" = "<contents of <data-dir>/local.secret>" }
```

The secret rotates when the data dir is recreated; a stale value gives a 401
with no other symptom, so re-copy it if Codex suddenly sees zero tools.

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

### Wiring it: `~/.codex/hooks.json`, not `config.toml`

Codex reads hooks from **`~/.codex/hooks.json`**, a separate file from
`config.toml`. Missing that is why this stayed unwired long after the variant
existed: the MCP server was configured, the hooks file was never created, and
nothing anywhere said a wake path was absent.

Copy [hooks.json](hooks.json) to `~/.codex/hooks.json` (merge, if you already
have one). No restart: Codex rebuilds its hook registry when the config changes.

`dibs doctor` reports whether any harness has actually called this daemon's
hooks, which is the only proof that a wake path is live rather than merely
present on disk.

**Until it is wired, Codex mail is pull-only and the wake digest surfaces to the
HUMAN instead of the agent it is addressed to.** That is how this was noticed
twice: a person watching their own Codex prompt fill up with mail for
`codex-primary`, and later the same thing reported as "it's putting it on my
plate to take an action for them to notice". Dibs no longer attaches the digest
to a human's prompt on any harness (`UserPromptSubmit` carries nothing to the
model), so an unwired Codex is now quiet rather than misdirected: quiet is
honest, and `dibs doctor` names it.

### When a Codex hook takes effect (traced, not assumed)

Codex does NOT require a session restart to pick up new hooks, and saying it
does was our error. `Session::refresh_runtime_config`
(`core/src/session/mod.rs:1701`) rebuilds the hooks config and calls
`HooksRegistry::reconfigured`, which is documented as preserving in-flight
background hooks while applying a refreshed configuration, then stores it into
the LIVE session.

What triggers it is the part that matters, and it is narrower than "any config
change". `reload_user_config` (`app-server/src/request_processors/config_processor.rs:295`)
walks every live thread and refreshes each, and it has exactly two callers:

| Trigger | Path |
|---|---|
| `ConfigBatchWrite` with `reload_user_config: true`, when the edits are not session-defaults-only | `config_processor.rs:157` |
| `experimental_feature_enablement_set`, when something actually changed | `config_processor.rs:172` |

Both are mutations made THROUGH Codex's own app-server API. We found no watcher
on `config.toml` (the app-server ships `codex-file-watcher`, used by
`skills_watcher`), so a hand-edited TOML is not known to refresh a running
thread.

The practical consequence for setup instructions: telling someone to edit
`~/.codex/config.toml` means telling them to restart, while a hook installed
through the app-server API applies to every running thread immediately. That is
the deeper integration worth having, and it is a harness capability we are not
using yet.

The historical reasoning is kept below, because it is why we refused to solve it
with a `command` hook, and that refusal still stands.

## Why we would not use a `command` hook (still true)

Codex's `HookHandlerConfig` once had exactly three variants, `command`, `prompt`
and `agent`, and this file argued at length that none of them could reach Dibs
without turning it into a harness driver. That argument was correct and is now
history: `mcp_tool` exists, and the section above wires it.

What survives is the refusal it rested on. We will not close a gap with a
`command` hook: a CLI reformatting mail into the harness's continuation protocol
is Dibs driving the agent. See [PHILOSOPHY.md](https://github.com/agenxy/dibs/blob/main/PHILOSOPHY.md).

This section is kept short deliberately. It previously stated, as current fact,
that "there is no `mcp_tool` and no `http` variant", directly under a section
explaining that `mcp_tool` had arrived. Both were checked in, and the shipped
`hooks.json` was written against the true one, so a reader comparing the two
had no way to tell which described the product.

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

