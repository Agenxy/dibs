# Native ChatGPT app delivery

Dibs first offers each notice to the existing desktop app when the recipient
agent was registered from ChatGPT and uses the canonical local Codex queue
route. One follower-start-turn request lets the app start or steer. The app decides
turn admission. Dibs starts no app-server, router, harness or other agent process.
It does not open a window after native acceptance.

An absent socket, explicit owner-discovery `no-client-found`, or a proven
failure before input selects the existing cold queue/open route, after checking
the original owed work again. That route can navigate the app. A socket
write, queue admission and matching native app acceptance are distinct receipts;
none proves that the agent consumed mail. Before-input failures (connection,
initialization or discovery) never strand an owed notice solely because the
native adapter failed. If board freshness itself cannot be established, no
stale input is sent and the existing bounded failure retry remains. A matched
owner refusal after input remains explicit, with no queue fallback. Only a
missing or garbled reply after input-frame bytes were written
is unknown and prohibits an automatic retry. Even a partial input write is
conservatively submitted. Unknown original inputs are reserved within one receiving app
incarnation. New mail qualifies immediately; an observed new app incarnation
clears those reservations and re-offers outstanding work once. Losing derived
evidence on a daemon restart permits a bounded recovery offer.

## Measured version and proof boundary

The source trace used installed ChatGPT **26.1002.52244**, build **13536**, bundled
Codex **0.162.0-alpha.2**, on 2026-10-09. Its `app.asar` SHA-256 was
`40efd7acdf03a24817fcd7f35684fc2173b154df06774243cb4ab227e36fa915`,
checked before and after extraction and before the approved live proof. Codex
upstream was separately fetched at `4aa94dce270de668eff6e2fa8585c82385e84455`.
Upstream source is evidence of design, not behaviour of the installed binary.

An approved single live input into an already owned, idle AGENT thread produced
a matching successful start reply at **2026-10-09T22:42:28.535Z**. A new turn
started at **22:42:28.453Z**, and the target rollout recorded the exact approved
factual notice at **22:42:28.626Z**. The prior completed turn and new turn IDs
were distinct. Raw framed requests/replies and the corresponding rollout lines
were retained as local investigation receipts. No person's thread was used.
This establishes one owned-thread start, not a fleet or focus guarantee.

A separately approved read-only own-thread probe measured a **10,490,345-byte**
snapshot, arriving in **312 ms**, on 2026-10-10 UTC. That exceeded the former
8 MiB adapter cap and reproduced the live installation failure. Dibs now never
follows a thread or fetches its history. The installed app's start wrapper
delegates to `turn/start`; the harness chooses start-or-steer atomically.

One approved live start request into this agent's active thread returned its
existing in-progress turn ID `01a12321-d42a-74e0-88c1-cabcdb0d82ee`. The rollout
recorded the exact approved notice and no new task start after the baseline.
This proves one active-turn admission, not a fleet or focus guarantee. The
adapter reports **accepted** with the matched turn ID, never guesses whether
that input started or steered a turn.

The receive safety bound matches the installed protocol's 256 MiB limit.
Unsolicited frames larger than the 1 MiB metadata/receipt buffer are drained
without retaining their bodies, preserving the next frame boundary. Discarded
bytes never establish acceptance; if a matched receipt is absent or oversized,
the outcome stays unconfirmed. A 50 MiB streaming fixture allocated less than
2 MiB including its producer; the observed allocation was about 35 KiB. No app
navigation is part of these probes or guards.

A separately approved cold discovery produced the exact router refusal
`no-client-found`, with zero input requests. An earlier 7-second timeout was
shorter than the router's 10-second discovery timeout and proves no refusal.
The corrected probe allowed 20 seconds. Neither changed the target rollout.

## Wire contract

Resolve `CODEX_HOME` or `~/.codex` anew per attempt and connect to
`ipc/ipc.sock`. Socket and immediate parent must belong to the current user,
with no group/other permissions; symlinks and non-sockets are refused. Frames
are a uint32 little-endian byte length followed by UTF-8 JSON. No shell or app
launcher participates.

| Method | Version | Purpose |
|---|---:|---|
| `initialize` | 0 | Obtain the app router's client ID |
| `thread-owner-discovery` | 1 | Find an existing owner for `hostId: local` and the agent's conversation ID |
| `thread-follower-start-turn` | 2 | One text input with `inheritThreadSettings: true`; app chooses start-or-steer |

Requests have fresh IDs and replies must match request ID, method and target
owner. Dibs advertises no owned threads and answers discovery with `canHandle:
false`. It requires `supportsUntrustedAppInput: true` from the owner. Start
acceptance requires the nested turn ID and in-progress status. The same receipt
represents starting or steering, so Dibs reports accepted. Returned IDs are
private verification data, never board state.

The source anchors in the measured archive are byte offsets, not line numbers:
`src-BPM2XJL0.js` around 14174 (versions);
`bootstrap-CTpobVUg.js` around 1745519/1747766 (router timeout/refusal),
1752240/1753550 (broadcast/request), 1763495 (registered owner/follower handlers),
273908/274900 (following/snapshot), and 759820/1080260 (start/steer wrappers);
`main-B6ZOwXa3.js` around 697808/720824 (snapshot serialization).

## Mail and remote boundaries

The sender is included only when it matches `^[a-z0-9][a-z0-9-]{0,62}$`;
other names are omitted entirely (`Dibs: new question.`). The event type is
restricted to Dibs' fixed vocabulary. The payload is an event and sender fact, such as `Dibs: new request from
reviewer.` It contains no body and no imperative. Names travel only over private
IPC, never command arguments. Immediately before input, the writer rechecks
original outstanding items and the recipient incarnation/session. Handled work
settles without input. Normal mail consumption remains an authenticated MCP
call, designed for the stateless 2026 protocol first.

A current host bridge advertises `com.dibs/native_app_delivery: true` in its wake
subscription. Old bridges cannot advertise it accidentally and retain their
existing in-flight policy. The current bridge selects its local route and asks
`POST /api/wake-owed` with its existing board credential immediately before
input. The hub answers only for that pending request's stated bridge host and
original recipient/items. Foreign-host, unknown-request and unauthenticated
calls get the same generic refusal. This is the existing shared-secret host
trust boundary, not cryptographic host attestation (see SECURITY.md).

## Cold loading and upstream request draft

The installed source exposes follower handlers for threads already owned. No
external load-without-navigation interface was found in this trace; that is a
source boundary, not proof that no such interface can exist elsewhere.
`codex://threads/<id>` invokes `ensureWindow({forNavigation:true})` and
`navigate-to-route` (main offsets 3386410/3381630; bootstrap 2209448).
`open -g` does not prevent the app from selecting a chat or revealing its window.

### After an app restart

A restarted app has loaded no agent thread, and its socket refuses
connections for some seconds while it comes back (about five, measured
2026-10-10 on 26.1002.52244). Before this, both of those took the cold route,
so every agent's next mail navigated the person's window, at moments nobody
chose, once per agent. Now:

- A refused dial while the app's process is up is a restart, not a fault. The
  mail is held: no queue, no open. A refused dial with the process gone is a
  stale socket, and the cold route launches the app as before.
- On each new app incarnation (two stable process samples), Dibs waits for the
  socket to accept, then loads every local ChatGPT-app agent thread in one
  pass under the desktop-wide open/restore lock: owner discovery first, an
  open only for a thread not held, a bounded wait for it to load, and then
  `codex://threads/new`, which returns the primary window to `/`, the route a
  launch starts on (read from the app's deep link handler: no path, origin or
  project means no workspace lookup and no dialog). A pass that opened nothing
  moves nothing.
- Only then are restart notices and held mail delivered, natively, into loaded
  threads. Until the pass ends, a wake that finds its thread unloaded is held
  too, so the one bounded retry cannot open a thread the pass is about to load.

What this cannot do is restore a chat the person opened during those first
seconds. The app exposes no focused-thread query over IPC, persists no route,
and honours its automation broadcasts only from its own `desktop` client,
which Dibs does not impersonate. The pass therefore runs as early as the
socket allows and returns to the launch view, which is where the window
stands unless the person has already moved it.

**Draft for upstream review; not posted:** Please expose a versioned local IPC
operation that loads an existing local agent conversation into the desktop
app's owning runtime without selecting its chat, revealing its window or
changing focus. It should return a matched ownership/loading receipt and an
explicit refusal, preserve the thread's model/permissions/workspace, and allow
ordinary owner-targeted start/steer after success. Cold delivery currently needs
navigation even though already owned conversations accept native follower
input. A daemon must never start its own app-server as a substitute.

Claude Desktop's measured `ccd_session_mgmt.send_message` is an in-process,
session-scoped native tool. The read-only trace of Claude **2.26454.2** found no
external daemon-callable listener. That unresolved boundary does not justify
impersonating a session or inventing a transport. Closed-session recovery now
opens the generated background link immediately. That fallback can navigate or
activate the app and supplies no native message receipt; running sessions need
no open. Retiring the presence/idle delay does not resolve the external
messaging boundary.

**Claude draft for upstream review; not posted:** Please expose an app-owned,
authenticated local interface through which external coordination software can
deliver an explicitly untrusted factual notice to an existing Claude Desktop
Code-tab session, closed or running, without starting another harness,
impersonating a Code-tab session, selecting a chat or revealing a window. The
app should retain admission, authorization, ownership and running-turn
decisions, matching the native capability already available through the
in-process `ccd_session_mgmt.send_message` tool.

Return a matched receipt distinguishing accepted input, app-owned queueing,
refusal and an unknown outcome. A message ID alone does not prove a turn
started: a turn-start receipt should include a turn identifier or corresponding
start event. Unknown post-submission outcomes must remain explicit so callers
can avoid blind duplicate submissions. Stale or archived targets should receive
an explicit refusal rather than silently selecting another session. The
existing immediate background-link fallback does not supply these guarantees.
