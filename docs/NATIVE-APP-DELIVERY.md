# Native ChatGPT app delivery

Dibs first offers each notice to the existing desktop app when the recipient
agent was registered from ChatGPT and uses the canonical local Codex queue
route. It starts an idle owned thread or steers its active turn. The app decides
turn admission. Dibs starts no app-server, router, harness or other agent process.
It does not open a window after native acceptance.

Only an absent socket or an explicit owner-discovery `no-client-found` selects
the existing cold queue/open route. That route can navigate the app. A socket
write, queue admission and matching native app acceptance are distinct receipts;
none proves that the agent consumed mail. Malformed protocol, changed method
versions, refusal and missing replies fail explicitly with no queue fallback or
automatic retry. Unknown original inputs are reserved within one receiving app
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

The owner-targeted snapshot selection and steer envelope are source-validated
and exercised with independent framed Unix-socket fixtures. They have **not**
been measured against a live busy thread. The app may include full conversation
history in a snapshot; real snapshot size/shape remains unmeasured. Dibs bounds
a frame at 8 MiB and fails explicitly above it; the app's own bound is 256 MiB.
No additional live input or UI navigation is part of these tests.

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
| `thread-stream-following-changed` | 1 | Temporarily follow that owner for its current snapshot; unfollow on exit |
| `thread-stream-state-changed` | 11 | Owner broadcast with snapshot and `threadRuntimeStatus` idle/active |
| `thread-follower-start-turn` | 2 | One text input with `inheritThreadSettings: true` |
| `thread-follower-steer-turn` | 1 | One text input and required restore-message envelope; the app derives expected turn |

Requests have fresh IDs and replies must match request ID, method and target
owner. Dibs advertises no owned threads and answers discovery with `canHandle:
false`. It requires `supportsUntrustedAppInput: true` from the owner. Start
success requires the nested turn ID and in-progress status; steer success
requires the nested turn ID. A start racing an active turn may be admitted as a
steer by the app. Returned IDs are private verification data, never board state.

The source anchors in the measured archive are byte offsets, not line numbers:
`src-BPM2XJL0.js` around 14174 (versions);
`bootstrap-CTpobVUg.js` around 1745519/1747766 (router timeout/refusal),
1752240/1753550 (broadcast/request), 1763495 (registered owner/follower handlers),
273908/274900 (following/snapshot), and 759820/1080260 (start/steer wrappers);
`main-B6ZOwXa3.js` around 697808/720824 (snapshot serialization).

## Mail and remote boundaries

The payload is an event and sender fact, such as `Dibs: new request from
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
impersonating a session or inventing a transport. Closed-session recovery's
separate idle policy is tracked for the immediately following change.
