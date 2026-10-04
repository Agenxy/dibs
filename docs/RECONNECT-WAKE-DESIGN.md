# Reconnect wake recovery

Request 26671: after restarting the ChatGPT app, idle agents with mail stayed
idle. Request 26726 adds honest send receipts and a measurement of background
loading. This is a derived delivery repair, not session management.

## Evidence and limits

The installed Codex Desktop runtime reports 0.160.0. On 2026-10-04 an isolated
copy of this agent's pending queue and rollout survived two separate launches
of that installed runtime's read-only `app-server --listen stdio://` observer:
the same queue item remained, and `thread/loaded/list` stayed empty. This proves
queue persistence across observer restarts, not the operator's whole-app
restart. The live app started at 03:09:57; the subsequent snapshot contained
only newer items, so it cannot establish whether older items were dropped.

Upstream queue processing wakes loaded threads only. The installed app bundles
`thread/resume`, but that alone is not proof of an externally reachable load
API. Its default app-server control socket currently refuses connections. A
separate observer process must never resume a thread: that would host the
agent outside the app. Background loading remains investigation only.

There are two inferred holds: the daemon's per-agent queued-wake map and the
runner's per-thread receipt on disk. The native runner already honors an
observed empty queue over its fallback receipt, and honors an actual pending
Dibs notice. Reconnect must preserve that distinction.

## Pre-turn reconnect door

The Codex MCP handshake has no canonical thread ID; it arrives on tool calls.
Waiting for one cannot recover an idle thread. A local stdio bridge therefore
attaches its own process ID to every stateless request, including discovery;
legacy initialization gets the same additive metadata. Remote conversations
do not assert a local process.

The daemon independently probes that process's ancestry, within a bounded
deadline, and obtains the ChatGPT app root's PID and start time. A changed
`(host, app PID, start time)` identifies a reconnect cohort. It selects only
existing non-retired rows belonging to that host and already established as
ChatGPT-app sessions; it does not infer identity from a directory, assign a
session, change a PID binding, or ledger the observation. Multiple bridges
from the same app incarnation do not retrigger the cohort. Losing the cache
at daemon restart permits one bounded recheck, not loss of coordination state.
The process probe fixes locale and timezone so bridges and daemon observe the
same start stamp even when their inherited environments differ.

## Recovery decision

For each selected row with actionable outstanding mail (including delivered
but unconsumed mail and outstanding notices), invalidate only inferred queued
receipts and reconsider delivery through the existing normal route. Check
the mailbox again when the decision actually executes. No mail means no
wake. Existing cooldown and running-command exclusion apply. Coalesce the
cohort to one reconsideration per row; an authoritative pending queue item is
retained rather than duplicated. Even with a retained item, the ordinary
loaded-thread/opening step must be reconsidered, which is what matters if
restart only unloaded the thread.

Receipt invalidation compares app-incarnation generations, not wall-clock
ordering. A clock moving backward cannot keep the old receipt active. An
authoritative pending item renews the generation without adding a queue entry,
so a later unavailable observer retains that confirmed hold. A cancelled
startup or failed receipt write leaves the cohort retryable; the same
generation keeps already recovered items coalesced.

The operator confirmed the existing away/loaded-thread policy in message
26907: unloaded threads open only while the person is away, with no immediate
open option or new setting. No background-load policy is implemented by this
note. Claude SessionStart already polls and delivers pending mail;
it must not gain a competing socket writer.

## Honest sender note and verification

A confirmed queued wake must be described as queued in the app, running when
the thread is open or while the person is away. Merely having a configured
queue command does not prove acceptance; before the command finishes, say
that delivery is being attempted. Preserve pull-only diagnoses on failure.

Enter regression tests through bridge startup and the real HTTP MCP handler,
then the actual queue executable fixture. Simulate app incarnation replacement
with retained versus lost queue contents, including unavailable observation.
Cover stateless discovery first and legacy initialization, a future-dated
receipt after clock rollback, and an observer becoming unavailable after it
confirmed the retained item.
Verify no model call is required, no duplicate from a second bridge, no wake
for an empty mailbox or another host, and unchanged identity/ledger serial.
Run the new tests against the parent to prove failure before the fix, then the
full gate. The native app remains untouched by the simulation.
