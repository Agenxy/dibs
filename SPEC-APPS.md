# Dibs board panel (MCP Apps)

The board is a spatial thing: who is live, what they claim, what is waiting.
Rendering it as JSON into an agent's context serves neither reader well: the
model pays for detail it does not need, and the human reads a wall of braces.

MCP Apps ([SEP-1865](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/1865),
extension spec `2026-01-26`) fixes exactly that split, so Dibs implements it.

## Why it fits Dibs specifically

The spec draws a line we already care about:

- **`content`** (text for the model. Costs context.
- **`structuredContent`**) data for the UI. Explicitly **not** added to model context.

So on a host that renders, the model gets a single line and the human gets the
whole board.

**But that split is conditional, and an earlier draft of this document overstated
it.** `structuredContent` is not an MCP Apps invention: it is a **base MCP
field** whose normal destination is the model. MCP Apps reuses it and expects
UI-capable hosts to route it to the iframe instead. On a host that declares the
capability but does not render, it falls back to its base meaning and lands in
model context after all.

Measured against Claude Desktop, 6 agents:

| | bytes |
|---|---|
| `content` (model always pays | **72** |
| `structuredContent`) panel, or model if rendering fails | **1537** |
| worst case (no render) | **1609** |
| what the same call cost before this feature | **2123** |

So the payload is trimmed to exactly the fields `board_app.html` draws. The win
when rendering works is large; when it fails we are still cheaper than the plain
tool we replaced. That is the honest claim, and `panel_test.go` enforces the
trim.

## The panel rides along: it is not a separate step

A human should not need the agent to make a ceremonial "now show it" call. Every
tool that already carries board or mailbox state opens the panel:

| tool | opens on | why |
|---|---|---|
| `check_in` | board | the mandatory once-per-activation checkpoint |
| `inbox` | mail | reading mail should show the mail |
| `await_events` | board | it returned *because* something changed |
| `board` | its `view` | the explicit request |

This adds no data. `check_in` already returned board + inbox. It re-shapes what
the call produced, which is why it *reduced* context cost rather than adding any.

Note the hook cannot do this. `hook_poll` returns `additionalContext`: injected
text, not a rendered tool call. It can tell an agent mail arrived; only a tool
call can draw. The flow is: mail arrives → hook injects "2 unread" → the agent
calls `inbox` → the panel opens on that mail.

## What is implemented

| Piece | Where |
|---|---|
| `ui://agents/board` template, `text/html;profile=mcp-app` | `internal/mcp/board_app.html` (go:embed) |
| Resource listing + read | `internal/mcp/apps.go` |
| `board` tool with `_meta.ui.resourceUri` | `internal/mcp/tools.go` |
| Result shaping (`content` + `structuredContent`) | `showBoardResult` |

The panel shows every agent with a live/dormant/stale dot, its description, its
slots with `refs` and `dirs` as chips, a `persistent` badge, a `you` badge on the
caller's own agent, and relative last-seen times. A **Mail** tab lists the
caller's messages colour-coded by type (question / request / notify / handoff)
with sender and serial. `board(view:"mail")` opens straight onto it, so
"show me my mail" does not land on the board and make the human hunt for a tab.

## Design decisions

**The template is static.** Hosts prefetch and cache by URI, so baking a board
snapshot into the HTML would serve stale state forever. All live data arrives via
`ui/notifications/tool-result`. A test asserts the template contains no board
data.

**No external origins.** The declared CSP has empty `connectDomains` and
`resourceDomains`. The panel renders only what the host hands it and talks solely
to the host over `postMessage`. Nothing is fetched, so nothing needs allowing.

**Degrades to text.** On hosts without MCP Apps, `_meta` is ignored and
`board` still returns its summary line. Nothing breaks; the human just does
not get the panel.

**The web board stays.** `dibs web` remains the broad, whole-world view. The
panel is the in-conversation view of *this agent's* board.

## Host support

### Retraction: the UI capability is NOT required to render

An earlier version of this document (and of `panelResult`) stated that MCP
Apps "requires the client to declare
`capabilities.extensions["io.modelcontextprotocol/ui"]`", and gated
`structuredContent` on that declaration.

**That is wrong, and the gate has been removed.** Measured against
`modelcontextprotocol/ext-apps` `examples/basic-host`, the reference AppBridge
implementation other hosts are built on, the initialize request is:

```json
{"protocolVersion":"2025-11-25","capabilities":{},
 "clientInfo":{"name":"MCP Apps Host","version":"1.0.0"}}
```

It declares **no capabilities at all**, and renders the panel anyway. Hosts
dispatch off the tool result's `_meta.ui.resourceUri`, not off a declared
extension. Gating on the declaration silently starves every such host: the panel
draws, empty, which is indistinguishable from a host bug, and that is exactly
how three rounds of "the panel is blank" were misdiagnosed.

The cost of erring the other way is bounded and measured (see the byte table
above): a host that cannot render pays ~1.5 KB, still less than the 2.1 KB the
same call cost before this feature existed. **Bounded waste beats a feature that
silently does not work.** `panel_test.go` now asserts the payload is always
present, with the reasoning inline so the gate is not reintroduced.

Declarations captured from live handshakes (`DIBS_LOG_RPC=1`): still useful for
knowing what a client *says*, just not for deciding what to send it:

| client | declares `io.modelcontextprotocol/ui` |
|---|---|
| `claude-ai/0.1.0` | yes (`{mimeTypes:["text/html;profile=mcp-app"]}` |
| `local-agent-mode-agents/1.0.0` | yes) same |
| `claude-code/2.1.219` | no |
| `MCP Apps Host` (ext-apps reference) | **no, and renders regardless** |

**Claude Desktop supports MCP Apps.** An earlier draft said it did not; that came
from a CLI `claude -p` capture generalised to a surface it was never measured on.

| surface | client | panel renders |
|---|---|---|
| Claude Desktop **chat** | `claude-ai/0.1.0` | **yes**, rendered inline |
| Claude **Code pane** (inside Desktop) | `claude-code/2.1.219` | no, surfaces as text |
| **ext-apps reference host** | `MCP Apps Host` | **yes** |

The gap is surface-specific, not app-wide.

### Stateless HTTP hosts: `Mcp-Session-Id`

A browser-based host connecting over streamable HTTP has no stdio bridge to
carry per-connection state. `internal/mcp/session.go` issues an `Mcp-Session-Id`
at initialize and remembers what that connection declared, so a later
`tools/call` on a fresh HTTP request still knows which client it belongs to.
Capped at 512 sessions. This no longer gates the panel, but it is what makes any
per-connection decision possible at all on that transport.

### The template is cached by URI: a fix does not reach a connected host

**This is the trap worth knowing before you build an MCP App.** Hosts prefetch
`ui://…` at connect and cache by URI, exactly as the spec permits. A stable URI
is required for that caching to work, and it means **changing the template's
content is invisible to any already-connected client.**

Measured: a `board` call at 03:25:00 triggered **zero** `resources/read`.
The host was still rendering the copy it fetched when it connected at 01:03,
two hours and several fixes earlier. Two rounds of "the panel is blank" were the
*same old template* both times; the fix had never been delivered.

Consequences for anyone developing one of these:

- You cannot iterate on a template against a live host. Every change needs a
  client reconnect, or you are testing something you shipped hours ago.
- A blank panel tells you nothing about the template you just wrote.
- `notifications/resources/updated` exists for this (Dibs advertises
  `resources.subscribe` on both handshake paths) but no shipping client
  subscribes yet, so in practice reconnection is the only delivery mechanism.

This is the third staleness trap in this project: a stale daemon serving 19 tools
instead of 24, a stale bridge unable to forward a capability, and now a stale
template cache. All three produced confident, wrong conclusions. **When a
measurement crosses a process boundary, establish what that process was built
from before trusting what it reports.**

### The panel was blank, and it was our bug

The Desktop session reported a panel had rendered. It had not: the human saw an
**empty container**. That session inferred success from a host confirmation
message rather than from seeing anything, and this document repeated it. Two
retractions in one feature is a pattern: a rendering claim is only worth making
when someone has looked at pixels.

Diagnosed by reproducing the host's constraints locally: the template loaded in
sandboxed iframes under four CSP regimes, from none to `default-src 'none'`:

| CSP | result |
|---|---|
| none | shell renders |
| `script-src 'none'; style-src 'none'` | unstyled, but renders |
| inline allowed, no `data:` | shell renders |
| inline + `data:` | shell renders |

**CSP was not the cause**: nor the 68 KB of base64 font, nor the inline script.
Every regime drew something. What every regime also showed was the actual fault:
the panel sat on `connecting` with an empty body, because `draw()` was called
only *after* the `ui/initialize` promise settled: a **10-second timeout** when a
host does not answer. A host that sizes its container to content sees an empty
box and shows the human nothing.

The panel now paints on load and treats the handshake as an enhancement:
first paint never depends on a reply that may not come, the timeout is 2.5s, and
the idle state says `waiting for board data` rather than a `connecting` it cannot
promise. `apps_test.go` asserts `draw()` precedes the handshake.

### What the rendering surface then found

Driving it from Desktop chat produced a better bug report than any amount of
local testing: **the panel rendered AND the model still received the whole board
as JSON**, contradicting `board`'s own description.

That was ours, not the host's. When the `check_in` regression was fixed,
`board` was routed through the shared panel path, which deliberately
preserves the agent's full result, because `check_in` **needs** it: reading the
board is the awareness gate. `board` has no such need; the human is looking
at the panel. It now returns a summary line and nothing else when a renderer is
present:

```
Dibs board: 8 agent(s), 0 active. Shown to the human in the board panel.
```

Two further findings from the same session, both fixed:

- **`since_serial: 0` was a footgun.** It is the only cursor an agent can pick
  before it has seen the board, so every agent reaches for it, and once the ring
  floor moved past 0 it failed with `E_CURSOR_TOO_OLD`, an error about buffer
  internals the agent has no way to know. It now means "from the floor". A stale
  *non-zero* cursor still errors, because there the agent had a position and
  genuinely missed events.
- **The stale-recipient warning arrived after commit**, warning about what might
  happen to a message already sent. It now states what is true: *delivered to X,
  which is currently dormant: it will see this when it next wakes. The message is
  not lost; only the response deadline is at risk.*

## Contextual views, not one dashboard repeated

Rendering the same board on every call turns the panel into wallpaper. Each tool
opens the view that matches what it just did, and a call with nothing to report
draws nothing at all:

| tool | view | when it stays silent |
|---|---|---|
| `check_in` | board | never, orientation is always deliberate |
| `inbox` | mail | **empty mailbox draws no panel** |
| `await_events` | activity (capped at 40) | a timeout with no events draws nothing |
| `send` / `respond` | mail | (|
| `board` | its `view` argument |) |

Getting there exposed the named-map-type trap for the third time in this file:
`core.Result` does not satisfy `case map[string]any`, and `[]core.Event` does not
satisfy `.([]any)`, because Go treats named types as distinct. That has silently
produced a wrong answer three times: a summary reporting 0 agents over 7, a board
dropped entirely, and a mailbox panel suppressed while holding mail. The payload
builder now normalises through JSON once at the boundary, which removes the class
rather than patching the instance.

## Verified against the real host, not a simulator

The panel was originally checked against a locally-written host simulator. That
was the wrong instrument: a simulator only ever confirms the protocol *you
believe* is correct, and every blank-panel misdiagnosis in this file came from
believing something wrong. It has been replaced by
`modelcontextprotocol/ext-apps` `examples/basic-host` (the actual AppBridge
reference implementation) driven in a browser against the live daemon.

Reaching it needed one shim: a bun proxy on :3001 that injects the
`X-Dibs-Local` secret (the host knows nothing about our auth header) and answers
CORS preflight. The daemon, the template, the payload and the handshake are all
real.

Against that host:

- `resources/list` advertises `ui://agents/board` with the correct MIME type.
- `resources/read` returns the template plus `_meta.ui.csp` and `prefersBorder`.
- `tools/list` carries `_meta.ui.resourceUri` and `visibility: ["model","app"]`.
- `tools/call board` returns summary + `structuredContent` + `_meta.ui`.
- The panel renders **11 agents and 6 messages** of live board state, with the
  identity chip (`claude-opus-5`) and the `this panel` badge on the caller.
- `view:"mail"` opens straight onto the Mail tab.
- `ui/update-model-context` lands: the host's Model Context pane shows
  `Dibs: 5 unread message(s) for agent "host-test"`.

**Interaction is verified end to end.** Clicking **Approve** on request `#384` in
the panel issued a real `respond` call; the daemon then reported
`state: "approved", consumed: true, responded_serial: 390`, and the panel's mail
count dropped 6 → 5 on the next render. The panel is not a picture.

One caveat worth recording for anyone repeating this: synthetic clicks do not
cross into the reference host's nested cross-origin sandbox iframe, so the click
above was driven through a same-origin host speaking the identical postMessage
protocol and forwarding every call to the same daemon. Rendering was proven in
the reference host; actions were proven through that same-origin driver.

Earlier findings from the simulator era, all still guarded by tests:

- `resources/list` advertises `ui://agents/board` with the correct MIME type.
- `resources/read` returns the template plus `_meta.ui.csp` and `prefersBorder`.
- `tools/list` carries `_meta.ui.resourceUri` and `visibility: ["model","app"]`.
- `tools/call board` returns summary + `structuredContent` + `_meta.ui`.
Two bugs were found by rendering it rather than reasoning about it: the summary
counted `0 agents` while handing the UI seven (asserting `[]any` against a typed
slice yields nothing, silently), and the mail panel showed "No mail" over a full
mailbox (`inbox` arrives as `{messages:[…]}`, not an array). Both now have tests.
