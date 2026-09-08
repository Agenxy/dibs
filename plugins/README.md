# Dibs plugins

Dibs is an MCP server, so most harnesses need only a config entry. These folders
hold the per-platform specifics, and, where a harness offers a way to deliver
mail without Dibs driving anything, the integration that does it.

## The server delivers these

An agent connecting to Dibs is told, once, on its first registration, that a
plugin exists for the harness it just named, and `dibs://plugin` carries the
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
   thread or session. Dibs is a service agents pull from. A plugin may decide
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
| [codex](codex/) | MCP over **stdio** | ✅ `mcp_tool` hooks run on builds from 2026-08-18 | **yes**: the only harness on MCP 2026-07-28 end to end |
| [chatgpt-desktop](chatgpt-desktop/) | shares Codex config | ❌ inherits Codex | no |
| openclaw | not yet assessed | not yet assessed | deferred |

## Transport: why these differ, and why that is not an inconsistency

Read this before proposing that a harness be moved onto a different transport.
The table above is deliberately mixed, and the mixture is the design.

**MCP 2026-07-28 defines two standard bindings and deprecates neither.** stdio
is newline-delimited JSON-RPC over a subprocess's standard streams; Streamable
HTTP is one POST per message. The transport that IS deprecated is **HTTP+SSE**,
the old two-endpoint one, which is a different thing and easy to confuse with
Streamable HTTP. Protocol semantics are identical on every binding, including
`subscriptions/listen`, so nothing is forfeited by choosing either.

stdio is not the legacy option. Its metadata model is the one 2026 defines,
everything inline in `_meta` with no header layer, and the spec tells custom
transports over Unix sockets or TCP to reuse its framing.

**Codex is on stdio on purpose.** It was moved there from HTTP, and moving it
back would be a regression:

- The stdio bridge is one process per session with a filesystem, so it is what
  can hold an agent's nonce across a context boundary. That is the difference
  between a returning session reattaching and forking a sibling that cannot read
  its predecessor's mail. Measured on a real board before the bridge existed:
  **nine rows for five roles.**
- Codex reaches MCP 2026-07-28 over stdio today, end to end, with no fallback.
  There is no protocol argument for moving it.

Identity itself is no longer transport-bound: a nonce may be pinned in the
harness's own config (`X-Dibs-Agent-Nonce` header, or `DIBS_AGENT_NONCE` env
which the bridge forwards), so an HTTP client can reattach too. That makes the
transport a genuine per-harness choice rather than a constraint. It does not
make the choices interchangeable: the bridge still supplies the nonce
automatically for stdio harnesses that pin nothing, which is most of them.

**So the rule is: pick the binding a harness supports best, and leave the others
alone.** A patch that unifies them for consistency's sake is trading a working
identity guarantee for symmetry, and we will turn it down. See CONTRIBUTING.md.

## What the survey found

**Three harnesses can wake an agent without Dibs driving anything**: Claude
Code (`mcp_tool` lifecycle hooks), opencode (in-process plugin hooks) and pi
(`before_agent_start`, which can inject a message). All three were verified in
live turns, not in isolation. pi was the surprise: it has no MCP client at all,
and turned out to have the cleanest wake hook of the three.

Everywhere else mail is **pull-only**: `await_events` / `inbox`, at the agent's
choosing. That is the honest floor and it works on every surface.

**Codex is no longer the near miss.** It was, while `HookHandlerConfig` offered
only `command`, `prompt` and `agent`: the paragraph here used to say one new
handler variant would flip it. Builds from 2026-08-18 have the `mcp_tool`
executor, so the shipped hooks run and mail is injected. See
[codex/README.md](codex/) for what to check on a given build, and note that a
hook only ever reaches an agent that is RUNNING: reaching a stopped one is
`[wake.exec]`, which is the operator's configuration and not a plugin.

**Codex reaches Dibs; its model provider is what blocks execution.** Codex
connects over streamable HTTP and enumerates every tool into an `mcp__dibs`
namespace: that half is proven from a captured request payload. But it sends
the tool list using OpenAI Responses-API types (`web_search` as a server tool,
and `namespace`-typed groups) that OpenRouter's Responses shim rejects with
`400 Server tool request failed`. Codex namespaces MCP tools unconditionally
(`codex-rs/core/src/tools/spec_plan.rs`); there is no config flag to flatten
them. So driving Codex needs a provider that implements those types. Nothing
here is a Dibs limitation.

**Identity is observed, never self-reported** (SPEC §5.0). Driving real models
showed every observable field arriving blank, `{"cwd":"","branch":"",
"model":"","session_id":"","pid":0}`, so the bridge fills in what it can see.
Where a harness's MCP client announces its SDK rather than itself (hermes
arrives as `{"name":"mcp"}`), set `DIBS_HARNESS` in that harness's own MCP
config; a client that identifies itself always wins.

**A woken agent needs to be able to answer.** Discovered by driving opencode: a
lifecycle hook can tell an agent it has mail, but a fresh turn carries no token,
so the agent re-registered and became a *sibling* agent that could not read the
mail that woke it. `register` now reattaches when the same name and
`session_id` are presented, returning a fresh token for the existing agent. Any
new wake path needs this, or waking an agent only frustrates it.
