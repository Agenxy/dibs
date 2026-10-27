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

Errors the app's router writes itself (it could not route the request)
carry no `method` and no `handledByClientId`, read from 26.1007.30912's
router: `no-client-found` (on owner discovery, the thread is not loaded; on a
targeted input, no client received it), `request-version-mismatch` (refused),
and `request-timeout`, `client-disconnected`, `server-closed` (outcome
unknown). A client's own reply must still match the request's method and
owner.

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

### Failed or still-unconfirmed local queue loading

Queue admission and a successful OS open callback remain separate from a
started turn or authenticated mail consumption. On 2026-10-26 the production
log recorded `agenxy-dev-0` queue acceptance at 14:20:02.163Z, an app-open
callback at 14:20:02.343Z and native acceptance at 14:20:31.039Z. Supply later
withdrew its claim that a manual open was required: its request to the person
was around 14:27Z and it held no manual-open receipt. Earlier native acceptances
also prevent classifying the whole 24–26 October interval as unloaded. None of
these receipts alone establishes when mail was consumed.

A second report on 2026-10-27 (Supply request 344810) illustrates why the
observation must stay time-bounded. The local log recorded a queue command
acceptance for `agenxy-dev-0` at 03:37:17.077Z and another at 04:02:58.787Z.
The app's own thread history subsequently showed a turn starting at 04:03:05Z
from the latter Dibs notice, with an actual `read_mail(344808)` call completed.
For `agenxy-labs-2`, native acceptances were recorded at 04:02:51.320Z and
04:03:36.321Z; the original app thread showed a turn starting at 04:03:36Z and
an authenticated check-in. Both were already running when inspected, before
any manual recovery open was issued. This confirms current recovery in the
original visible threads; it neither proves an uninterrupted earlier stall nor
attributes recovery to a human open. In this report the Dev 0 queue observation
also had unknown pending/thread state, so the retained-known-pending guard
below does not claim to classify that earlier attempt.

Every wake must run its turn in the original app thread the person can inspect.
A headless successor or resume process does not satisfy that requirement; an
app-owned session-messaging route can, when it starts the visible session's turn.

The separate reproducible defect was an OS open failure after queue acceptance:
it was logged, but the configured queue route prevented ordinary human contact.
The local engine now escalates captured unread asks on that failure. A later
mail event that observes an existing Dibs item still pending beyond the same
ten-minute window already used for app-open recovery retries that load path
and escalates continued unconfirmed delivery. It never enqueues a duplicate
for the retained item. This is an event-driven observation, not a scheduled
poll or a promise to recover an otherwise quiet seat at ten minutes.

The sender receives the exact original thread link and the human gets the
existing metadata-only contact alert. Late callbacks are fenced by identity,
session, configuration, queue observation and app incarnation; handled mail
cannot be resurrected. The sender's separate warning rebuilds from contact
state and retains its authenticated read prefix. Remote host-bridge open
callbacks are a separate transport boundary; this local repair does not claim
a new remote app-open receipt.

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
- Ownership is measured again at the end of the pass: a navigation may finish
  asynchronously while later targets load. An incomplete measurement warns
  with the threads still unowned or whose ownership could not be measured,
  without another open or a longer per-thread wait. Settled means the pass
  ended, including after a failure; it never means every thread loaded. Later
  mail for an unloaded thread can then take the ordinary queue/open route.
  A late pass cannot settle a different app incarnation. Restart notices and
  held mail can reach owned threads natively.

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

The process budget is another observed boundary. A read of Claude
**2.26454.2** (`app.asar` SHA-256
`e9628d73410e4771ae5e9ac509ea20e61e8eb312f18a8fc10c222f9b094f9787`)
found a soft CLI-process cap of `max(6, floor(total RAM / 3 GiB))`; a warm
spawn at that cap yields rather than starting a process. This is a finding
about that exact source image. Separately, runtime logs on **2026-10-14**
recorded two warm reopen attempts for one Code tab yielding at the cap, and
Dibs subsequently recorded that no process appeared during its 30-second
settle window. Later explicit input did start the process. A successful open
link therefore establishes neither process admission nor notice delivery.

**Claude draft for upstream review; not posted:** Please expose an app-owned,
authenticated local interface through which external coordination software can
deliver an explicitly untrusted factual notice to an existing Claude Desktop
Code-tab session, closed or running, without starting another harness,
impersonating a Code-tab session, selecting a chat or revealing a window. The
app should retain admission, authorization, ownership and running-turn
decisions, matching the native capability already available through the
in-process `ccd_session_mgmt.send_message` tool.

Return a matched receipt distinguishing accepted input, app-owned queueing,
refusal and an unknown outcome. If the app's process budget prevents a closed
tab from resuming, report explicit queueing or refusal; acknowledging an open
link is not admission. A message ID alone does not prove a turn
started: a turn-start receipt should include a turn identifier or corresponding
start event. Unknown post-submission outcomes must remain explicit so callers
can avoid blind duplicate submissions. Stale or archived targets should receive
an explicit refusal rather than silently selecting another session. The
existing immediate background-link fallback does not supply these guarantees.
