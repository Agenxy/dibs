# Dibs for Codex

Codex reaches Dibs through `dibs mcp-stdio`, one bridge process per session.
That is not ceremony: the bridge is what holds this agent's nonce, so a
returning session reattaches instead of forking a sibling that cannot read
its own mail. The url form is documented below for a client that cannot run
a process at all, and it costs an identity per session.

## Install

In `~/.codex/config.toml`:

```toml
# BOTH parts. The feature alone leaves the connection on 2025-06-18.
[features]
mcp_2026_07_28 = true

[mcp_servers.dibs]
command = "/absolute/path/to/dibs"
args = ["mcp-stdio"]
# MCP 2026-07-28. Codex speaks it, but only when BOTH the `mcp_2026_07_28`
# feature is enabled AND this exact variable is set on THIS server entry. The
# feature alone leaves the connection on 2025-06-18, and a wrong value here is a
# hard error rather than a fallback.
env = { CODEX_MCP_PROTOCOL_VERSION = "2026-07-28" }
```

`dibs mcp-config` prints this with the real path filled in.

**stdio rather than a url, and the difference is an identity.** The bridge is one
process per session, and it is what remembers this agent's nonce, so a returning
session reattaches to the same agent with its mail instead of forking a `-2`
sibling that cannot read a word of its predecessor's. Measured on a real board
before the bridge existed: nine rows for five roles.

That is a default, not a cage. A nonce can also be pinned in this file
(`env = { DIBS_AGENT_NONCE = "..." }`, or an `X-Dibs-Agent-Nonce` header over
HTTP), which is what lets an HTTP client reattach at all. The bridge remains the
automatic path for a harness that pins nothing, which is the common case.

**Please do not send a patch moving this to HTTP for consistency with other
harnesses.** It is the most reasonable-looking change in this directory and we
would turn it down: stdio is not the legacy binding, MCP 2026-07-28 keeps both
current and deprecates neither, Codex reaches 2026 over stdio end to end today,
and the automatic identity path would be lost for every operator who pins
nothing. The reasoning is in `CONTRIBUTING.md` under "Changes we will turn down"
and in the transport section of `plugins/README.md`. If you think it is wrong,
an issue is cheaper for you than the PR, and the argument is welcome.

This file used to print the url form, Codex took it, and the cost was invisible
for months. A board carrying nine rows for five roles is what it looks like from
outside.

The url form is for a client that cannot run a process at all. It is NOT the
answer for another machine: run the bridge THERE, pointed at the hub, which is
what `dibs mcp-config --board <addr>` prints. A remote session is the
long-lived unattended one, so a forked identity costs most exactly there.

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

## Waking an agent: not yet, but it is being built, and we should wait for it

**This section has flipped twice on the strength of reading a type, so this
time it is dated, and the evidence is runtime behaviour.**

State on **2026-08-17**, against codex main `32a383c0` and the Codex Desktop
`0.148.0-alpha.9` binary inside ChatGPT.app:

| | |
|---|---|
| `mcp_tool` hook config parsed | yes, since `81b9bc21` (2026-08-07, #37363) |
| hooks engine has an `mcp_tool` handler | yes, since `85fc4def` (2026-08-15, #38705) |
| a real session supplies an MCP executor | **yes, since 2026-08-18 (#39296)** |
| so an `mcp_tool` hook actually runs | **yes, on a build from that date or later** |

That last wire was missing for three days and this file spent months saying so.
`codex-rs/core/src/session/mod.rs` built the `HooksConfig` for every real
session with `mcp_executor: None`, and the engine dropped every `mcp_tool`
handler at startup:

```rust
if mcp_executor.is_none() {
    ... "skipping MCP tool hook in {}: MCP invocation is not available yet"
}
```

On a build older than 2026-08-18 you will still see `skipping MCP tool hook in
~/.codex/hooks.json`, once per entry, and the hook does not fire. Upgrade or
accept pull-only delivery; nothing here can work around it.

**So Dibs ships `hooks.json` for Codex**, and it is at the ROOT of the config
directory: `~/.codex/hooks.json`, not `~/.codex/hooks/hooks.json`, which is
Claude Code's layout and a path Codex does not read. It binds `hook_poll` on
SessionStart, Stop and SubagentStop. Measured against a live daemon on
2026-08-22: three hooks, three deliveries.

Two limits worth knowing before you rely on it. A hook fires only if the Dibs
server is ALREADY connected, and connections are established asynchronously, so
SessionStart can lose that race while Stop is later and reliably wins. And a
hook is a callback on YOUR OWN lifecycle: it delivers when your turn ends, and
nothing outside can make an idle thread wake through it. Reaching a thread that
is not running is `[wake.exec]`, which is the operator's business and documented
in `docs/CONFIGURATION.md`.

**And we still do not reach for the alternative.** Codex declares four handler
types and runs two. `prompt` and `agent` are empty structs, skipped by name
("prompt hooks are not supported yet"). That leaves `mcp_tool`, which Dibs uses,
and `command`, a subprocess, which Dibs will not: shipping subprocess glue is
shipping the thing `WAKE-MECHANISMS.md` §6 rejected.

**How to check what YOUR build does, which is not the enum.** The enum was there
before any of this worked, and reading it is what produced two wrong conclusions
in this file, in opposite directions. Ask a session, not the source:

```
codex --version                 # 2026-08-18 or later has the executor
```

and then `spawned_agents`, read by a SECOND agent while this thread is between
turns: `state` reading `finished` for the session is what a delivered Stop
leaves behind. Being listed proves nothing on its own, because SessionStart
creates the listing too, and you cannot check it yourself: a tool call needs a
turn, and your next turn opens with SessionStart, which puts `state` back to
`running` before you can look. If no peer sees `finished`, the build predates
the executor or the Dibs server was not connected when the hook ran.

**On an older build Codex is pull-only.** `check_in` at the start of every
activation, `await_events` before blocking, which is what `dibs://skills`
already tells every agent. Mail is not lost either way: it waits, and the
`waiting` line on every authenticated result names it on the next call. The
digest does not reach the human on any harness.

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

Dibs ships a MAIL hook file for Codex and no supervision one. The second half of
that is a decision rather than a gap.

Codex can also fire hooks as SUBPROCESSES, and that is the type Dibs will not
use. A hook that shells out makes Dibs a thing that drives your harness, which
[PHILOSOPHY.md](https://github.com/agenxy/dibs/blob/main/PHILOSOPHY.md) rules
out; `mcp_tool` calls a tool on the connection the model already holds, spawns
nothing, and is the only type in the shipped file.

If your build predates 2026-08-18 the hook is skipped and mail is pull-only:
call `check_in` at the start of an activation and `await_events` when you are
about to block. That is the honest floor, it needs no configuration, and it
works everywhere, hook or no hook.

Two earlier versions of this document were wrong in opposite directions, which
is why the dates above are exact. The first described a `hooks/hooks.json` that
had never worked: six of its seven entries used a type no session supported, and
the seventh was the subprocess this project refuses to be. The second said Dibs
ships no hook file for Codex at all, and went on saying it after one shipped,
because the note outlived the fact it was written about. A file that reaches
agents through `dibs://plugin` is documentation with a reader who cannot check
it, so it says what is true today and when that was measured.

