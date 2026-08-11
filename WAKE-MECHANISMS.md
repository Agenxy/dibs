# Waking an agent: what actually works (decision record)

**Question:** when a peer sends a Lanes message, how does the recipient agent find out,
without Lanes shelling out, driving the harness, or orchestrating sessions? Lanes is a
coordination *service* agents use, not a harness.

**Answer as of 2026-07-25: solved for Claude Code, without a subprocess.** Its hooks
support `type: "mcp_tool"`: a lifecycle hook calls a tool on the MCP connection the
model already holds, and the tool's `hookSpecificOutput.additionalContext` is injected
into the model's context. No shell, no second process, no polling. Lanes ships this in
its plugin (`plugins/lanes/hooks/hooks.json` → `hook_poll`).

**Amended 2026-07-26: opencode is the second, and it was driven live.** An
in-process opencode plugin on the `chat.message` hook injects mail as a synthetic
message part. Verified in a real turn with a real model, which read the mail and
replied unprompted. See `plugins/opencode/`.

**And waking is not enough on its own.** That same live run exposed the missing
half: a woken agent has no token, so it re-registered and became a sibling lane
that could not read the mail that woke it. `register_lane` now reattaches on
matching name + `session_id`. A wake path without reattach only frustrates the
agent it wakes.

Everywhere else the agent still pulls (`await_events`). Evidence below.

**Claude Desktop is NOT covered by that solution.** Desktop has no lifecycle hook
system at all: hooks are a Claude Code feature, and a plugin's `hooks/hooks.json`
is inert in Desktop. So the `mcp_tool` wake does not exist there; Desktop is
tools-only, pull-only. We do not close the gap with a shell hook (rejected, §6).
See `plugins/claude-desktop/README.md`.

**Corrections to earlier drafts of this doc** (both were wrong, found by reading source):
1. *"All hook mechanisms are subprocesses"*: true of Codex (`HookHandlerConfig::Command`
   is the only executing variant), FALSE of Claude Code, which has five handler types:
   `command`, `http`, `mcp_tool`, `prompt`, `agent`.
2. *"Codex never calls resources/list, so Lanes' resources are invisible there"*: wrong.
   Codex registers `list_mcp_resources` / `read_mcp_resource` as model-facing tools
   whenever any MCP server is configured, and they issue real `resources/list`. opencode
   exposes the same three tool names. Resources are pull-visible in both.

---

## 1. Measured, not researched

A daemon with `LANES_LOG_RPC=1` recorded exactly what each client sends when it connects
over plain HTTP (no stdio bridge in the way):

| Harness | Version | Handshake | Declared capabilities | Methods sent |
|---|---|---|---|---|
| Claude Code (desktop engine = the app's own build) | 2.1.219 | `initialize` **2025-11-25** | `roots`, `elicitation` | initialize, tools/list, resources/list |
| Claude Code CLI | 2.1.218 | `initialize` 2025-11-25 |, | initialize, tools/list, resources/list |
| Codex | 0.144.1 / **0.146.0-alpha.7** | `initialize` **2025-06-18** | `elicitation {form,url}` | initialize, tools/list |
| opencode | 1.18.4 | `initialize` **2025-11-25** | `roots` | initialize, tools/list |
| Copilot CLI | 1.0.75 | 2025-11-25 |, | tools only |
| Pi | latest | **no MCP at all** |, |, |

**Nobody sends `subscriptions/listen`, `resources/subscribe`, or `resources/read`.
Nobody speaks MCP 2026**, not even Codex alpha, which negotiates 2025-06-18. Codex never
even calls `resources/list`, so Lanes' resources are invisible there; only tools reach it.

## 2. Is 2026 support hidden behind a flag? No

- Claude Code 2.1.219: `2026-07-28`, `server/discover`, `subscriptions/listen` → **0
  occurrences**. No MCP-protocol env flag exists.
- Codex alpha: its embedded Rust MCP SDK lists `2026-07-28` in the ProtocolVersion enum.
  **Amended 2026-07-25 (this was wrong):** a feature flag now DOES exist,
  `Mcp20260728` ("Enable MCP protocol version 2026-07-28 support") in
  `codex-rs/features/src/lib.rs`, config key `mcp_2026_07_28`. Off by default, so
  the measured 2025-06-18 handshake stands for stable 0.145.0.
  **TESTED 2026-07-25: the flag does NOT change the wire.** With
  `mcp_2026_07_28 = true` resolved true (`codex features list`), a source build of
  codex `61a4488` connecting to Lanes over HTTP still negotiated
  `protocolVersion: 2025-06-18`, and sent ZERO `server/discover` and ZERO
  `subscriptions/listen`. It saw all 24 tools over the legacy path. Codex marks the
  flag "under development"; it gates unfinished work, not a protocol switch.
- opencode (1071 branches): no `subscriptions/listen`, no `2026-07-28` anywhere.

Reasonable: the 2026 spec was still an RC (final 2026-07-28). Expect movement after.

## 3. Methodological caveat: do not trust strings in binaries

An earlier draft of this doc claimed "all clients implement `resources/subscribe`" based
on grepping binaries. **That inference was wrong.** opencode's source disproves it: the
shipped 1.18.4 binary contains `notifications/resources/updated` (an **SDK schema
constant**) while opencode's actual handler is on an unmerged branch, and a handler that
*is* in main shows zero string hits. In bundled/compiled binaries, string presence proves
nothing either way.

**Trust only:** (a) live RPC probes, (b) real source.

## 4. The one harness actively building it

opencode branch `feat/mcp-resource-updated` (2026-06-08, **not merged**):

```ts
if (capabilities?.resources?.subscribe) {            // server must advertise this
  client.setNotificationHandler(ResourceUpdatedNotificationSchema, async (n) => {
    events.publish(ResourceUpdated, { server: name, uri: n.params.uri })
  })
}
```

Gated on the server advertising `resources.subscribe`. Siblings:
`feat/mcp-resource-list-changed`, `fix/mcp-prompts-list-changed`. When this merges,
opencode is the first harness that can receive a Lanes push: provided we advertise the
capability (we now do, on the **legacy** handshake, which is the one every client uses).

Codex, by contrast, handles `resource_updated` as a **tracing log only** (inert to the
model) per `codex-rs/rmcp-client/src/logging_client_handler.rs`.

## 5. What Lanes implements

- **`await_events`**: a Lanes tool that blocks server-side (parks a waiter, event-driven,
  ≤60s) and returns a **batch** of everything since the caller's cursor. Works on every
  MCP host today. This is the floor and the product.
- **`subscriptions/listen`** (SEP-2575): held-open SSE, acks with
  `notifications/subscriptions/acknowledged`, then pushes
  `notifications/resources/updated` for `lanes://inbox` (token-scoped via
  `_meta["com.lanes/token"]`) and `lanes://board`. Verified end-to-end. Unused by clients
  today; ready for the 07-28 wave.
- **`resources.subscribe` advertised on BOTH handshakes**: including legacy
  `initialize`, since that's the path 100% of clients take. Advertising only on
  `server/discover` made the capability invisible.

**Not built (the next bet):** legacy `resources/subscribe` / `unsubscribe` + a GET SSE
channel to deliver `notifications/resources/updated` on 2025-11-25. This is what pays off
first, because it's what shipping clients speak.

## 6. Rejected approaches, and why

| Approach | Verdict |
|---|---|
| Shell hooks (`lanes codex-hook`, plugin `monitor`) | **Deleted.** A CLI reformatting mail into the harness's continuation protocol is us driving the agent, a wrapper, not a service. |
| Codex app-server `turn/steer` (true mid-turn inject) | Rejected: requires *owning* the thread over a Unix socket = orchestration. |
| `@openai/codex-sdk` supervisor | Rejected: TS-only (no Go SDK), and it means managing sessions. |
| Claude **Channels** (`notifications/claude/channel`) | Genuinely elegant (zero agent steps) but **CLI-only** (platforms matrix), so unavailable in the Desktop app; research-preview + allowlist. |
| `Monitor(ws)` tool | Works in Claude Desktop (one agentic tool call, WebSocket push, no shell) but is Claude-specific. |
| MCP notifications / elicitation as a wake | Notifications are inert; **elicitation targets the human**, not the model (and SEP-2260 forbids unsolicited server→client requests). |

## 7. Honest limits

- "Interrupt me mid-work the instant mail arrives" is **not** achievable natively today
  without a shellout or thread ownership. We don't fake it.
- MCP 2026 is deliberately moving *away* from unsolicited push (SEP-2260); even the Tasks
  extension is pull-then-stream and presupposes the agent called a tool first.
- So: the agent receives mail when it **chooses to listen** (`await_events`) or at a
  natural boundary. Per-surface push is an enhancement layered on top, never the floor.

Related: [[CODEX-WAKE-FINDINGS.md]] (Codex app-server protocol research; note its §(c)
should be amended. MCP *elicitation* IS forwarded to Codex's UI, though it targets the
human, not the model).
