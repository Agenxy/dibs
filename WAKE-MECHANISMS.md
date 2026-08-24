# Waking an agent: what actually works (decision record)

**Question:** when a peer sends a Dibs message, how does the recipient agent find out,
without Dibs shelling out, driving the harness, or orchestrating sessions? Dibs is a
coordination *service* agents use, not a harness.

**Answer as of 2026-07-25: solved for Claude Code, without a subprocess.** Its hooks
support `type: "mcp_tool"`: a lifecycle hook calls a tool on the MCP connection the
model already holds, and the tool's `hookSpecificOutput.additionalContext` is injected
into the model's context. No shell, no second process, no polling. Dibs ships this in
its plugin (`plugins/claude-code/hooks/hooks.json` → `hook_poll`).

**Amended 2026-07-26: opencode is the second, and it was driven live.** An
in-process opencode plugin on the `chat.message` hook injects mail as a synthetic
message part. Verified in a real turn with a real model, which read the mail and
replied unprompted. See `plugins/opencode/`.

**And waking is not enough on its own.** That same live run exposed the missing
half: a woken agent has no token, so it re-registered and became a sibling agent
that could not read the mail that woke it. `register` now reattaches on
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
2. *"Codex never calls resources/list, so Dibs' resources are invisible there"*: wrong.
   Codex registers `list_mcp_resources` / `read_mcp_resource` as model-facing tools
   whenever any MCP server is configured, and they issue real `resources/list`. opencode
   exposes the same three tool names. Resources are pull-visible in both.

---

## 0. Can an outside process wake a thread that is already running?

Measured on 2026-08-22 against codex `8e649e3a` and the installed builds, after
an operator asked the question this document had never actually answered. The
short version: **not the way Dibs was reaching for, and not by default at all.**

Three surfaces exist, and only one of them wakes the ORIGINAL thread.

| Surface | Wakes the original? | Why |
|---|---|---|
| `mcp_tool` hooks | **Yes, at its own boundary** | Runs inside the thread. A callback on ITS lifecycle: nothing outside can trigger one |
| app-server `thread/resume` + `turn/start` / `turn/steer` | **Yes, if addressable** | Direct injection into a loaded thread. See below for why it usually is not |
| `codex exec resume <uuid>` | **No** | Starts a SUCCESSOR process on the same transcript. Two rows appear on the board |
| `codex queue --thread <uuid>` | Only if loaded | Enqueues durably and calls `wake_if_loaded`. Measured against an unloaded thread: returned `Queued message` and nothing stirred |

**Why the good one is usually unreachable.** The app-server owns live threads,
and its RPC surface has everything waking needs: `thread/loaded/list`,
`thread/resume`, `turn/start`, `turn/steer`, `thread/queue/add`. But the
Desktop app runs its OWN app-server as a child over private stdio pipes. Checked
directly: that process holds fds 0, 1 and 2 as anonymous unix socketpairs to its
Electron parent and listens on nothing. `~/.codex/app-server-control/
app-server-control.sock` exists but is a stale file from an earlier run; a
connect gets ECONNREFUSED and no process holds it.

So a thread in the Desktop app is not addressable from another process, by
construction rather than by oversight.

`codex remote-control start` is the supported way to change that: it runs the
app-server daemon with remote control enabled, `codex remote-control pair`
issues a short-lived pairing code, and `--remote` accepts `ws://`, `wss://` and
`unix://`. Threads that live in THAT daemon can be woken by an outside process.

**What this means for Dibs.** Waking the original thread is a property of how
the agent was STARTED, not something a coordination service can retrofit onto a
conversation already running inside a desktop app. An agent that must be
wakeable has to live in a remote-control-enabled daemon, and starting that
daemon is the OPERATOR'S step, the same as starting `dibd`.

Being exact about who starts what, because the three paths differ and the
difference is the whole argument:

- A **hook** starts nothing. The agent calls out at its own turn boundary.
- **`[wake.exec]`** is Dibs spawning a process: the operator's command, which
  for Codex starts a headless `codex exec resume`. Headless, and a SUCCESSOR
  rather than the original thread.
- **Remote control** would have Dibs open a socket to a daemon that is already
  running and ask it to start a turn. It launches nothing; if the daemon is not
  there, the wake does not happen and says so.

Dibs does not, and should not, launch a desktop application. Opening somebody's
GUI is a different product, and none of the mechanisms above need it.

## 1. Measured, not researched

A daemon with `DIBS_LOG_RPC=1` recorded exactly what each client sends when it connects
over plain HTTP (no stdio bridge in the way):

| Harness | Version | Handshake | Declared capabilities | Methods sent |
|---|---|---|---|---|
| Claude Code (desktop engine = the app's own build) | 2.1.219 | `initialize` **2025-11-25** | `roots`, `elicitation` | initialize, tools/list, resources/list |
| Claude Code CLI | 2.1.218 | `initialize` 2025-11-25 | none | initialize, tools/list, resources/list |
| Codex | 0.144.1 / **0.146.0-alpha.7** | `initialize` **2025-06-18** | `elicitation {form,url}` | initialize, tools/list |
| opencode | 1.18.4 | `initialize` **2025-11-25** | `roots` | initialize, tools/list |
| Copilot CLI | 1.0.75 | 2025-11-25 | none | tools only |
| Pi | latest | **no MCP at all** | none | none |

**Nobody sends `subscriptions/listen`, `resources/subscribe`, or `resources/read`.**
Codex did not call `resources/list` either when this table was measured, which is
corrected immediately below and was contradicted by it for a while: on 2026-07-28 it
does, and Dibs' resources are visible there. The table is a measurement with a date on
it, and the paragraph under it is what is true now.

**"Nobody speaks MCP 2026" was true when measured and is now false. Amended 2026-08-17.**
Codex runs entirely on 2026-07-28 against Dibs today. That took a fix here, and the
correction is the useful part: Codex ASKED for 2026, was answered in the legacy era, and
fell back to 2025 for every real call, because Dibs read the protocol version from an
HTTP header that stdio does not have. For a day that looked exactly like a client without
2026 support. See `TestStdioClientAskingFor2026IsServed2026`, and treat "the harness does
not implement it" as a hypothesis needing a daemon log, not a conclusion.

Two conditions on the Codex side, and neither is the default:

1. the `mcp_2026_07_28` feature enabled, and
2. `CODEX_MCP_PROTOCOL_VERSION=2026-07-28` in **that server's `env`** block.

The stdio rule is exact (`codex-rs/rmcp-client/src/protocol_mode.rs`): the feature alone
stays on 2025-06-18, and a wrong value is a hard error rather than a fallback. With both
set, Codex sends `server/discover` instead of `initialize`. Verified twice, against a
probe server that logged the raw method and against Dibs itself, where the agent then
called `board` and got the real board back.

**Claude Desktop carries the 2026 machinery too** (1.30096.5): an `era: "2026-07-28"`
wire codec, a `>= "2026-07-28"` version predicate, and a switch mapping `server/discover`
to that revision. Whether it negotiates 2026 with Dibs in practice is NOT yet measured,
and the distinction matters here more than anywhere: this file has twice recorded a
capability read out of a binary as though it were a behaviour. **Claude Code 2.1.219 does
not**: its only protocol constants are 2025-03-26, 2025-06-18 and 2025-11-25.

The lesson this table keeps teaching is that every row is true on its date and not after.
Per-harness re-checks are tracked as issues rather than as prose here.

## A wake delivers; it does not instruct

An agent hears about mail when it ARRIVES, not when somebody next types at it.
A fleet that waits for a person to kickstart its responsiveness is not
independent, and a time-sensitive request sitting unseen because nobody was at
the keyboard is the failure this whole product exists to prevent.

This was got wrong once, in the other direction, and the reasoning is worth
keeping because it is a plausible misreading of rule 5. `additionalContext` on a
`Stop` hook does more than inform. Claude Code's documentation says it "keeps
the conversation going". That looked like driving the harness, so delivery was
narrowed to work somebody was blocked on and everything else was held for the
agent's next activation.

That reads the rule wrong. **Driving a harness means instructing it.** The
digest says outright that it is coordination data from peers, not instructions,
which the agent may act on or decline: the agency is in the content and in the
agent's freedom to ignore it, never in withholding delivery until a human
appears. Waking an agent so it can decide is the opposite of controlling it.

What genuinely deserved the name was **nagging**, and that is a different fix:

- **Each message wakes its recipient once.** An agent that read something and
  chose not to act has exercised exactly the judgement the digest grants it, and
  re-waking it every turn would be taking that back.
- **Work somebody is BLOCKED on comes back**, on the same retry an
  unacknowledged announcement uses. A question nobody has answered is not a
  decision, it is a peer waiting, and the point of a deadline is that somebody
  notices before it expires.
- **`stop_hook_active` is honoured**, so a wake never continues a turn a wake
  already continued. That is a loop guard, not a preference, and no setting
  switches it off.

The throttle is keyed per message and bounded by what is unread, so a mailbox
that empties takes its entries with it.

### The knob

```toml
[wake]
extend_turn_for = "all"      # default: anything unread wakes the agent, once
# extend_turn_for = "urgent" # only work somebody is blocked on
# extend_turn_for = "none"   # never extend a turn; systemMessage and `waiting` only
```

`all` is the default because the alternatives trade awareness for tokens, and
that is a trade only the person paying should make deliberately.

The human is told either way. `systemMessage` goes to the person on every poll
with news, whatever was decided about the model, because "your agent has mail"
is exactly what an operator wants to know and it interrupts nobody.

## 2. Is 2026 support hidden behind a flag? Yes, behind TWO

- Claude Code 2.1.219: `2026-07-28`, `server/discover`, `subscriptions/listen` → **0
  occurrences**. No MCP-protocol env flag exists.
- Codex alpha: its embedded Rust MCP SDK lists `2026-07-28` in the ProtocolVersion enum.
  **Amended 2026-07-25 (this was wrong):** a feature flag now DOES exist,
  `Mcp20260728` ("Enable MCP protocol version 2026-07-28 support") in
  `codex-rs/features/src/lib.rs`, config key `mcp_2026_07_28`. Off by default, so
  the measured 2025-06-18 handshake stands for stable 0.145.0.
  **TESTED 2026-07-25: the flag alone does NOT change the wire.** With
  `mcp_2026_07_28 = true` resolved true (`codex features list`), a source build of
  codex `61a4488` connecting to Dibs over HTTP still negotiated
  `protocolVersion: 2025-06-18`, and sent ZERO `server/discover` and ZERO
  `subscriptions/listen`. It saw all 24 tools over the legacy path.
  **Amended 2026-08-17: that measurement was right and incomplete, and the
  conclusion drawn from it was wrong.** The flag is one of two conditions, not
  the whole switch. The second is an environment variable on the SERVER's own
  config entry, `CODEX_MCP_PROTOCOL_VERSION=2026-07-28`, and the stdio rule is
  exact (`codex-rs/rmcp-client/src/protocol_mode.rs`): the feature alone stays on
  2025-06-18, and a wrong value is a hard error rather than a fallback. With both
  set, Codex Desktop `0.148.0-alpha.9` sends `server/discover` carrying
  `2026-07-28`, and Dibs answers it. Verified twice, against a probe server that
  logged the raw method and against Dibs itself. "The flag does not change the
  wire" was a true observation that became a false conclusion the moment it was
  written as an answer rather than as a measurement.
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
opencode is the first harness that can receive a Dibs push: provided we advertise the
capability (we now do, on the **legacy** handshake, which is the one every client uses).

Codex, by contrast, handles `resource_updated` as a **tracing log only** (inert to the
model) per `codex-rs/rmcp-client/src/logging_client_handler.rs`.

## 5. What Dibs implements

- **`await_events`**: a Dibs tool that blocks server-side (parks a waiter, event-driven,
  ≤60s) and returns a **batch** of everything since the caller's cursor. Works on every
  MCP host today. This is the floor and the product.
- **`subscriptions/listen`** (SEP-2575): held-open SSE, acks with
  `notifications/subscriptions/acknowledged`, then pushes
  `notifications/resources/updated` for `dibs://inbox` (token-scoped via
  `_meta["com.dibs/token"]`) and `dibs://board`. Verified end-to-end. Unused by clients
  today; ready for the 07-28 wave.
- **`resources.subscribe` advertised on BOTH handshakes**: including legacy
  `initialize`. That was every client when it was written, and it is not now:
  Codex runs entirely on 2026-07-28 against Dibs. Advertising on both is still
  right, because the legacy path is the transitional courtesy PHILOSOPHY.md rule
  9 describes, and "100% of clients" is the sentence that went stale.

**Built, and this line said "not built" for a while:** legacy
`resources/subscribe` / `unsubscribe` and a GET SSE space delivering
`notifications/resources/updated` on 2025-11-25. Dispatch, the SSE space, the
legacy capability advertisement and the split transport are all in
`internal/mcp`. It was written as the next bet, it was taken, and the sentence
was not updated: a document that describes shipped work as unbuilt sends an
integrator looking for an alternative that is already here.

## 5b. The harness's own session socket (SHIPPED)

Claude Code publishes, per session, a unix socket and an authentication key,
both on disk, and accepts newline-delimited JSON on it. Dibs uses it.

**Why this is not the `turn/steer` the table below rejects.** That rejection is
about OWNING a thread: driving it, deciding what it does next. This sends one
sentence, the same one `[wake.exec]` carries, and then closes the connection.
It cannot read the thread, cannot steer it, and cannot see what happens next.
Delivery is mediated by the recipient's own harness, which labels the message
as coming from a peer, tells the model it is not user input, and gates it on
that human's permission mode.

**Why it is better than a command.** A command has to be told which thread to
resume, so Dibs has to work out which id the agent answers to, and every wake
defect this project has had is downstream of getting that wrong. A socket is
the address. It needs no operator configuration, spawns no process, and needs
no thread id, which was the single largest class of unwakeable agent.

**Measured, not inferred**, because §3 of this document exists. A message was
sent to a live session over this path and watched arrive; a wrong token
produced nothing, which is how we know auth is enforced. Three defects were
caught by that probe and by nothing else: `os.TempDir()` is the wrong base on
macOS, the liveness stamp is UTC while `ps` answers local, and the wire test was
flaky. Any one of them would have shipped a feature that silently never fired.

**What is unchanged.** One gate for both routes: the cooldown, the
still-running flag and the deferral are shared, because each was paid for by a
bug. No command and no socket is still no wake. No process is ever spawned for
a thread that cannot be resumed. The notice carries counts and senders, never a
body.

## 6. Rejected approaches, and why

| Approach | Verdict |
|---|---|
| Shell hooks (`dibs codex-hook`, plugin `monitor`) | **Deleted.** A CLI reformatting mail into the harness's continuation protocol is us driving the agent, a wrapper, not a service. |
| Codex app-server `turn/steer` (true mid-turn inject) | Rejected: requires *owning* the thread over a Unix socket = orchestration. Note §5b: delivering one notice over a socket the harness itself publishes is a different thing, and ships. |
| `@openai/codex-sdk` supervisor | Rejected: TS-only (no Go SDK), and it means managing sessions. |
| Claude **Spaces** (`notifications/claude/space`) | Genuinely elegant (zero agent steps) but **CLI-only** (platforms matrix), so unavailable in the Desktop app; research-preview + allowlist. |
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
