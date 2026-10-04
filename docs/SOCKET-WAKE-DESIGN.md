# Socket economy: request 26745

Measured 2026-10-03 PDT from three exact Claude session transcripts and
`dibd.log`. Each of 998 daemon socket handoffs matches one distinct native
peer receipt within 0.782 seconds. Read-only reproducer and aggregate JSON:
`/tmp/dibs-socket-economy-measure.py` and
`/tmp/dibs-socket-economy-measure.json`.

| Native presentation | Actionable mail | Informational | Due wait | Continuation | Total |
| --- | ---: | ---: | ---: | ---: | ---: |
| Peer user message | 206 | 654 | 13 | 0 | 873 |
| Queued during a turn | 50 | 68 | 4 | 3 | 125 |
| Total | 256 | 722 | 17 | 3 | 998 |

No human notify headers occurred in this sample. Classification reads only
digest envelope headers, never quoted sender bodies. Pending request,
question and handoff headers count as actionable. Notice-only agent updates
and FYI notify headers count as informational. Native user presentation is
not a proof of daemon idle state: no retained UserPromptSubmit receipt or
bridge success log is available. Therefore the historical number of truly
idle sessions cannot honestly be reconstructed from these files. Mid-turn
queued attachments are the native receiver's explicit evidence of activity.

## Accepted policy (architect answer 27174, 2026-10-04)

An idle session wakes for outstanding actionable mail (request, question,
handoff, or human notify) or a due declared wait. Coalesce those reasons into
one socket message, carrying all due slots plus the current mail digest.
A busy session gets no socket message for any reason: its Stop hook carries
the outstanding items and any due recheck. Informational-only mail (agent
FYI, progress, non-blocking DONE, queue-position updates) stays unconsumed until a hook or
the next actionable wake carries it. Continuation remains the existing Stop
hook contract, at most two per declaration version; the separately bounded
work-backoff contract needs an explicit decision rather than silent removal.

The three initially unclassified envelopes were declaration continuations,
not focus notices; correction sent in 27198. The observed candidate reduction
is 722 informational presentations plus 54 queued mid-turn actionable/due
waits, out of 998. This classifies that historical sample; it does not assert
that every remaining peer-user presentation was idle. Approval/answer/flag
notices and DONE to a currently waiting sender are exceptions under 27721;
historical waiting state was not measured, so 776 is a candidate count rather
than a promised saving for the final policy.

## Existing contract to reuse

The engine already owns current-session turn state. `noteTurnState` records
Stop/SessionEnd and retracts it on starting hooks and tool activity; old
session aliases cannot change the current turn. A continued Stop retracts
the ended marker too. Recency timeout alone is not an idle proof, especially
for a long-running turn. Socket eligibility must therefore use explicit
lifecycle state, retaining the current-session boundary and host identity.
Use three states: a current finishing hook establishes idle; a starting hook
or authenticated model/tool activity establishes busy and refreshes its age;
a finishing hook establishes idle. Absent observations are unknown. A blocked/continued Stop
retracts idle. These are derived observations, never core or ledger fields.

Recovery rule accepted in 27721: unknown after a daemon restart retains
the existing bounded boot/contact grace, then permits one coalesced wake for
actionable mail or a due wait. Unknown never qualifies informational-only
items, and logs must call it unknown rather than idle. This preserves idle
agents' reachability when the prior Stop was lost; its explicit limit is one
possible unnecessary wake before the first lifecycle observation. A known
busy turn never ages into idle. Review 28532 added a 30-minute silence ceiling:
busy becomes unknown when no starting/tool/permission hook or authenticated
call has refreshed it. A CLI, subagent or plugin can use the token outside the
session's turn, and a Stop can be lost; neither can permanently disable waking.
Fresh activity keeps a long turn busy. Requiring a new Stop in the unknown
case would strand already-idle sessions indefinitely.

The daemon and a claiming stdio bridge remain alternative writers. Do not
add a third writer or surrender the bridge merely because a particular
notification should stay on the hook path. Both writers must consult one
engine-owned eligibility decision immediately before writing; the digest
read and socket-offer handshake are the existing doors. Hook delivery must
continue to read complete outstanding mail even when a socket digest is
empty. No mailbox delivery or notice acknowledgement happens on suppression.

The bridge's stream gets a digest from `WakeDigestFor`; deferred delivery
uses `FreshWakeDigestFor` or the additive `SocketOfferFor` handshake. These
socket-specific entry points can return an empty digest for a running turn
or informational-only mail while Stop/SessionStart retain their own full
digest. The plain digest read remains non-consuming. Do not gate the shared
formatter used by hooks. Direct daemon socket plans need the same rule.

Coalescing must hold through the idle wake epoch, not merely fifteen seconds
or one message serial. Fence it to host plus current harness session, so
several agent mailboxes in one session still share one wake. Later pending
mail folds into the one offer until
actual turn-start evidence, a failed write, or a current-session change
re-arms it. The existing offer records already distinguish a socket write
from receiver acceptance. Neither a write nor an in-turn tool call proves
that the harness accepted a new peer turn.

Due-wait plans currently bypass the offer handshake in `sendPeerWake`, and
the self-waking stream currently observes mailbox events rather than due
work alone. They must enter the same eligibility/coalescing door. Do not
spend a due-wait retry on busy-session suppression. Stop must present due
slots in the agent's own words and continue through its existing output
contract, instead of assuming a mailbox digest already contains them.
Quote all qualifying due slots; do not call a later wait due merely because
a shorter wait is ready. Preserve the existing bounded recheck cadence.

Classification refined in 28814 after live use: answers, denials, declines,
flagged reviews and grant/adoption verdicts wake an idle agent. Approval of an
ordinary work request is informational, alongside progress, accepted reviews
and queue changes. The request's typed `grant` and `adopt` fields distinguish
an approval with an effect from acceptance of work; never match its prose.
DONE wakes an idle sender only when that sender currently holds a declaration
whose `waiting` field is set. Otherwise it rides the next hook or actionable
delivery. Evaluate the current declarations at wake time, never ledger this
derived eligibility. An agent waiting for a human's approval remains
reachable. Reuse canonical event/message classification, not a text search
or a duplicated mail-state table. The existing `Blocking` notice flag alone
is insufficient because it unconditionally includes DONE.

Dormant older bridges require explicit attention: they may hold one notice
scheduled before upgrade, and do not re-exec while idle. Future notifications
can carry an empty digest they already understand; current bridges refresh
at write time. A policy cannot silently assume every bridge implements the
newer offer handshake. State the bounded old-timer rollout limitation rather
than claiming zero historical writes immediately after installation.

## Proof contract

Enter through real MCP lifecycle hooks, actual stdio subscription delivery
and a private session socket fixture. Register and claim the bridge normally.
Record UserPromptSubmit/PreToolUse, send an actionable request, assert no
socket frame, then call Stop and assert the mail is delivered there. In a
separate idle session, call Stop, send a request and observe exactly one
socket frame. Add more pending mail before a turn starts and assert the same
idle wake epoch remains one frame. Verify FYI/progress/DONE/agent_updates
remain visible to a subsequent hook without a socket write. Exercise both
the self-waking bridge and daemon fallback, and the accepted due-wait policy.

Also drive a busy turn beyond the normal contact cooldown and assert that
actionable mail still produces no socket; two due slots produce one idle
socket; a due wait becoming ready during a turn reaches Stop without spending
its socket budget. A held peer keeps hook fallback. A failed write re-arms
eligibility, current-session movement cannot wake the old session, and an
unknown-after-restart fixture proves the stated bounded recovery behavior.
Test approval/answer-only notices separately from DONE and progress. DONE to
a currently waiting sender produces one frame; DONE without such a declaration
produces none, and the next hook still shows it. A second
mailbox in the same host/session must not create a second socket frame.

Measure future writes with metadata for lifecycle eligibility and mail class,
without message bodies or credentials, so the production result can be
audited directly instead of reconstructed from a recency guess. Run the
regressions against their parent, then the full gate, and submit one concrete
PR for architect review.

## Implementation and proof limits

`socket_policy.go` derives lifecycle and one wake epoch using authoritative
host ID plus current session (legacy hostname only when no host ID exists).
Both native writers refresh eligibility at the actual offer door. The claiming
bridge batches only independently authenticated tokens; daemon fallback
gathers its current same-host/session rows and stands down for a claiming
bridge. Kernel success settles the epoch; actual turn evidence confirms the
presentation for all quoted participants, without consuming raw mail.

`socket.ready` is a rebuildable hint on the existing modern subscription,
without a ledger serial or ring cursor. It makes due waits and unknown-after-
grace mail discoverable by the same writer. Per-slot clocks also cover an
approved request parked by a linked waiting declaration. Hook presentation
advances cadence without spending a native retry. Three successful due writes
retain the stalled-row and assigner report; the unchanged open-work backoff
is 10/30/60 minutes. A failed daemon write gets one retry for its current
actionable cohort, preventing the readiness tick from adding unlimited retries.

Real-door tests use the actual stdio server, private MCP daemon, real lifecycle
calls and a private session socket. The busy/FYI, due delivery, closed-harness
and strict-log guards fail on the parent while idle actionable delivery passes.
The tests also exercise canonical approval/answer/flag/DONE eligibility,
coalescing beyond the bridge cooldown, cross-host batch exclusion, actual failed
bridge/daemon writes, and three native due writes followed by the stalled row
and notification to the actual assigner. No eligibility setter establishes the
claiming bridge.

Known baseline gap, assigned separately by the architect in message 28415:
an unread flagged review after DONE can disappear from the derived notice
queue across a daemon restart. `codex-primary` owns state-derived review
reconstruction and its consumption watermark under request 26697; this change
does not claim that restart guarantee before that fix lands. An older dormant
bridge also retains the explicitly bounded pre-upgrade timer/single-mailbox
limitations above.
