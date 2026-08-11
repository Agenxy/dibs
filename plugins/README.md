# Lanes plugins

Lanes is an MCP server, so most harnesses need only a config entry. These folders
hold the per-platform specifics, and, where a harness offers a way to deliver
mail without Lanes driving anything, the integration that does it.

## The server delivers these

An agent connecting to Lanes is told, once, on its first registration, that a
plugin exists for the harness it just named, and `lanes://plugin` carries the
actual files plus an ordered setup procedure where every step says how to check
it took effect. Nothing here requires a checkout or network access.

That closes the gap these folders used to have: the plugins were real, tested and
documented, and agents never learned they existed, because the documentation
lived in a repository they had not cloned. A README nobody fetches is not a
delivery mechanism.

The daemon never claims to know whether you have already installed one: it
cannot see that. It offers the procedure and the checks, and the checks answer
the question.

## Three rules

1. **Never drive the harness.** No subprocess, no CLI shelling, no owning a
   thread or session. Lanes is a service agents pull from. A plugin may decide
   *when* to pull using a hook the harness already fires; it may not become a
   wrapper. See [PHILOSOPHY.md](../PHILOSOPHY.md).
2. **Plugins are not the product.** The daemon works with none of them. These are
   conveniences and delivery paths, shipped separately.
3. **Claim only what has been driven.** Every README below separates measured
   facts from unverified ones, and says which is which.

## Status

| Platform | Reach | Mail delivery | Driven live |
|---|---|---|---|
| [claude-code](claude-code/) | plugin + MCP | ✅ `mcp_tool` hook → `hook_poll` → `additionalContext` | yes |
| [opencode](opencode/) | MCP config | ✅ plugin, `chat.message` synthetic part | **yes**: real models, full mail loop |
| [pi](pi/) | **extension** (no MCP client) | ✅ `before_agent_start` injected message | **yes**, agent quoted the mail unprompted |
| [hermes](hermes/) | MCP via `hermes mcp add` | ❌ no hook system found | **yes**, every tool enumerated, real model |
| [claude-desktop](claude-desktop/) | MCP (stdio) or `.mcpb` | ❌ no hook system exists | tools yes; panel renders in the ext-apps reference host |
| [codex](codex/) | MCP over HTTP | ❌ hooks are subprocess-only | transport yes, every tool enumerated; execution blocked, see below |
| [chatgpt-desktop](chatgpt-desktop/) | shares Codex config | ❌ inherits Codex | no |
| openclaw |, |, | deferred |

## What the survey found

**Three harnesses can wake an agent without Lanes driving anything**: Claude
Code (`mcp_tool` lifecycle hooks), opencode (in-process plugin hooks) and pi
(`before_agent_start`, which can inject a message). All three were verified in
live turns, not in isolation. pi was the surprise: it has no MCP client at all,
and turned out to have the cleanest wake hook of the three.

Everywhere else mail is **pull-only**: `await_events` / `inbox`, at the agent's
choosing. That is the honest floor and it works on every surface.

Codex is the near miss: its hooks support `additionalContext` injection, exactly
the mechanism Lanes uses in Claude Code, but `HookHandlerConfig` offers only
`command` (a subprocess), `prompt` and `agent`. One new handler variant would
flip it. Re-check on upgrade.

**Codex reaches Lanes; its model provider is what blocks execution.** Codex
connects over streamable HTTP and enumerates every tool into an `mcp__lanes`
namespace: that half is proven from a captured request payload. But it sends
the tool list using OpenAI Responses-API types (`web_search` as a server tool,
and `namespace`-typed groups) that OpenRouter's Responses shim rejects with
`400 Server tool request failed`. Codex namespaces MCP tools unconditionally
(`codex-rs/core/src/tools/spec_plan.rs`); there is no config flag to flatten
them. So driving Codex needs a provider that implements those types. Nothing
here is a Lanes limitation.

**Identity is observed, never self-reported** (SPEC §5.0). Driving real models
showed every observable field arriving blank, `{"cwd":"","branch":"",
"model":"","session_id":"","pid":0}`, so the bridge fills in what it can see.
Where a harness's MCP client announces its SDK rather than itself (hermes
arrives as `{"name":"mcp"}`), set `LANES_HARNESS` in that harness's own MCP
config; a client that identifies itself always wins.

**A woken agent needs to be able to answer.** Discovered by driving opencode: a
lifecycle hook can tell an agent it has mail, but a fresh turn carries no token,
so the agent re-registered and became a *sibling* lane that could not read the
mail that woke it. `register_lane` now reattaches when the same name and
`session_id` are presented, returning a fresh token for the existing lane. Any
new wake path needs this, or waking an agent only frustrates it.
