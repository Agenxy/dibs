# Restart navigation verification, 2026-11-03

The reported failure was the app selecting a chat without apparent new activity.
The daemon log contained these restart passes:

| Recorded time | Chats opened | Target still unowned after the pass |
|---|---:|---|
| 2026-10-29 19:23:27 -07:00 | 11 | The same archived binding |
| 2026-11-01 03:29:44 -08:00 | 13 | That binding and one other |
| 2026-11-02 09:52:17 -08:00 | 10 | The same archived binding |

A read-only encrypted-ledger/snapshot replay identified the binding as an
archived agent. Archived rows are absent from the ordinary public board, but
the restart loop used `Retired()`, which intentionally excludes archived rows
so their retained mail can still reach them. The app's read-thread interface
explicitly reported no such thread; the local thread database and rollout
filenames contained no match. No production data or binding was changed.

The repair separates delivery from navigation. Quiet chats are not preloaded.
An archived agent can still receive native input in an owned chat; it cannot
open a window. Failed restart loading that ends explicitly unowned is retained
as a stale binding, honored by ordinary app opening too. Unknown probes and
refused navigations do not establish that diagnosis. Positive native acceptance
clears it. Doctor reads the same binding metadata as the public board, including
an archived binding that has no ordinary board row.

Remote native-only requests are capability gated on the hub. Older bridges
receive no archived app request they could turn into an open. A legacy Codex
binding without a surface is conservatively included: only its receiving host
can read the rollout header that proves its app origin. A capable receiver uses
that local evidence to retain native input without navigation; an explicit
terminal surface still outranks origin. Old bridges also receive no ambiguous
archived Codex plan. Mail remains readable while a bridge is upgraded or the
same agent reattaches with a current surface. A request already
handed to old code before archiving cannot be recalled by a new protocol field.

## Regression discrimination

All nine new guard files were copied byte for byte to a detached checkout of
`7ed9f5bed040822fe37f617548a581a8ecc65f53`. The old implementation produced the
following **18 intended failing leaf cases**; the candidate passed them all.

| Production door | Failing old cases |
|---|---:|
| `afterAppReturn`: quiet, held, archived, consumed during probe | 4 |
| Local archived cold opening | 1 |
| Archive during the actual ownership probe | 1 |
| Claude closed-session reopening | 1 |
| Hub handoff to old and native-only-capable bridges | 2 |
| Bridge native-refusal fallback and app opening | 2 |
| Legacy blank-surface hub handoff to old and native-only bridges | 2 |
| Legacy receiver owned input, no-owner fallback and app opening | 3 |
| Stale restart binding, later mail and restart | 1 |
| Doctor reading an archived binding without a normal board row | 1 |

The matched control runs took 25.268 s on old code and 15.486 s on the candidate,
including their test build/run overhead. These are discrimination receipts,
not an end-to-end performance benchmark. Owned archived native delivery remains
a passing positive control. Unknown ownership remains a negative control for
stale-binding classification. Quiet recipients' next actual mail still follows
the ordinary route. Refused restart navigation still settles and permits later
ordinary delivery. An old restart fixture lacking any recipient record was
corrected to register its recipient before asking for navigation.

An early Claude guard falsely passed old code because it retained fresh turn
evidence despite changing the status to archived. The corrected fixture expires
that evidence and drives the actual writer-side closed-session entry before
observing its diagnostic. Its old run records the actual forbidden app open.
No deadline, mail assertion, failure evidence or gate was weakened.

Self-review during the second whole gate found the legacy blank-surface case.
The gate was canceled after its normal race phase passed; it is not a whole-gate
pass. The first legacy probe incorrectly observed an automatic admission worker
before it had started. The corrected guard stores real mail and enters the
production plan factory and execution path, observing the actual remote request.
It fails both the old implementation and the earlier candidate at the unsafe
wire handoff. The receiver guard writes a real isolated rollout header and
asserts that app inference succeeds before exercising input and app opening.
The earlier candidate passes navigation refusal but fails owned legacy input;
the repaired candidate passes both properties.

## One native input and visible-selection observation

On installed ChatGPT **26.1027.61615**, one factual native notice was offered to
the implementation agent's already active, owned chat. The app accepted it in
**54 ms** and that active chat received the exact notice. Accessibility readings
immediately before and after recorded the same app process, frontmost bundle
and window title on a different selected chat. No app open or restart was part
of the probe, and no other agent received input.

The app build was **24141**, with `app.asar` SHA-256
`bb88615052255e79b5977885f9a0cc212c268589587c79d4d3b7c4b46e881a95`.

This measures one active-turn admission without an observed visible-selection
change. It does not prove idle-start behavior, delayed selection behavior, fleet
behavior, production installation or acceptance of the source repair. Private
receipts retain the request timing, matched turn receipt and window observations;
thread identifiers and the person's selected chat title are not published here.

The PR carries the separate whole-local-gate and hosted-CI receipts. Source
verification, merge, installation and live product acceptance remain distinct.
