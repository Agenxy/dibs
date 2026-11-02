# Recovery consent and autonomous delivery bookkeeping

Issue #557 blocks #556. This note distinguishes a reproduced mechanism from
the uninstrumented original Linux failure.

## Measurement

Linux at 76ae7c09 rejected survivor-initiated recovery with
`E_RECOVERY_STALE`, at `recover_agent_test.go:198`. Forty traced targeted runs
at main4693 passed (80 protocol subcases, 25.114 seconds). A bounded diagnostic
allowing one maintenance tick between preview/request, or request/approval,
failed both protocols at main4693: exit 1 in 6.521 and 5.732 seconds.

The preview/request trace differed in exactly the pending source message's
`contact_escalated_serial` (absent to 15) and the global serial (14 to 15).
Both agent rows were identical. This disproves the suggested source-liveness
mechanism for this reproduction. The approval reproduction changed the same
marker to 17; the request's recorded fence was also recomputed normally when
the send committed. The original hosted run did not trace its inputs, so its
exact changed field is not claimed as measured.

## Complete Message field audit

This audits the current `Message` declaration and its writers in core,
including daemon operations submitted by engine. Clear means **only in copied
v3 fence input**, never in stored state, replay records or the transferred mail.

| Fields | Autonomous writer / meaning | v3 decision |
|---|---|---|
| `ContactEscalatedAt` | `applyContactEscalate`, maintenance and failed-delivery contact; records a help attempt, not mail delivery | Clear. It changes neither custody, content, answerability nor the applicable deadline. A later expiry's actual state/detail stays fenced. |
| `StallNotifiedDeclaration` | `applyStallNotified`, atomic daemon notice plus suppression marker; the current engine has no emitter for this retained operation | Clear. Reporting the same owed work does not change that work or its ownership. The actual separate notice remains a normal message and is fenced wherever it belongs. |
| `State`, `Consumed`, `TerminalAt`, `ExpireDetail` | Delivery, expiry, displacement, automatic FYI consumption and participant actions | Keep. They change answerability, durable debt, visible diagnosis or retention/collection. Pending-to-delivered can start a response window. |
| `Deadline`, `ResponseWindowSec`, `NeverDeliveredAt` | Delivery starts the recorded response window; send records the never-delivered ceiling; sweep enforces them | Keep. These determine whether and until when an answer can be given. A mere attempt does not change them. |
| `DeliveredAt`, `DeliveredTime` | `applyMarkDelivered`, task-queue admission, authenticated mailbox/read/ack paths | Keep. Authoritative delivery/awareness evidence accompanies deadline activation and can make a notify disposable. It is not an attempted-delivery counter. |
| `NotifyAnnouncedAt` | `applyContactCheckpoint` after presentation/announcement | Keep. `disposableNotify` reads this marker directly; it can make a message eligible for displacement even without changing `Consumed`. |
| `NotifyConsumption`, `AckedAt` | `applyAckMessage` and bulk FYI consumption, including recorded presentation/reminder decisions | Keep. These identify actual consumption and terminal FYI state, affecting what remains available to move. |
| `RetainUntil`, `Tracked` | Retention is recorded with a successful mutating response; tracked sends select task retention | Keep. They control whether the message is retained/collectible. No maintenance pass rewrites the recorded retention decision. |
| `OutcomeReadAt`, `ReviewReadAt` | Authenticated full reads and recorded prefixes of outcomes/reviews actually presented by a delivery route | Keep. These select the unread outcome/review units a recovered identity will be shown. They are awareness state, not an attempted-delivery marker. Outgoing-only mail already projects only its sender rewrite, so another party's read bookkeeping there is already excluded. |
| `QueuePriority`, `QueueDebt`, `QueueRank`, `QueueBy`, `QueueChangedAt`, `QueueChangedSerial`, `QueueOrderLocked`, `QueueLockBy` | Queue admission/reordering, participant decisions and resulting reindexing of siblings | Keep. They control owed work, its order, locks and newly reportable queue outcomes. A rank change can be caused by another request but still changes the transferred work queue. |
| `Serial`, `From`, `To`, `Type`, `Body`, `SentAt`, `RequestPriority`, `Choices`, `Attachments`, `Grant`, `Adopt`, `Merge`, `Recovery`, `Milestones` | Creation/explicit request effects and authorized ownership changes | Keep. Identity, content, authority and ownership are the consent's subject. |
| `Response`, `RespondedAt`, `Progress`, `Deliverable` | Recipient work/report and sender milestone review | Keep. These are the actual outcome/report/review, including every `Progress` field (`Milestone`, `Note`, `Artifact`, `Review`, `By`, `Serial`, `At`). |
| `WithdrawalReason`, `WithdrawnBy`, `SupersededBy` | Author withdrawal/replacement; expiry does not invent one | Keep. These change the request's disposition or replacement relationship. |
| `AdoptedFrom`, `AdoptedAt`, `PriorAdoptionSerial`, `MergedSentAt` | Authorized adoption/merge and recorded completion | Keep. These establish incarnation-specific mail ownership and previously completed effects. |

Expiry is deliberate maintenance interference: if a response window expires
between request and approval, `State`, `TerminalAt` and `ExpireDetail` change
what can be answered and what will move. Consent must remain stale in that
case. Its corrective hint tells the caller to preview again and send a fresh
request with that fence. This is designed refusal, not a repeated delivery bug.

## Per-recipient delivery state outside Message

`Contacts` contains `MailboxFull`, `Serial`, `Recipient`, `OldestSerial`,
`HighWater`, `Count`, `WindowStart`, `NotifiedAt` and `ResolvedAt`. Maintenance,
OS receipts and resolution write these. The general merge projection already
excludes this separate contact-attempt table. The defect was its incidental
pointer inside the incoming message; v3 changes no contact records or delivery
receipts.

Space announcements contain `Serial`, `Space`, `From`, `Body`, `Acked`,
`Required`, `Retries`, `DepartedUnacked`, `State` and `MadeAt`. Retry/give-up,
member departure and acknowledgement update per-recipient status. Existing
supported recovery moves membership/ownership/queue inputs, not announcement
rows or their per-recipient acknowledgements. This patch changes neither that
transfer scope nor their existing exclusion from the merge fence. Message
announcements and consumption remain fenced for the reasons above.

Agent `Status`, `StaleReason`, `StaleSince`, `DormantSince`,
`LastCoordination`, `AckedSerial` and `ContactNoticeReadAt` can change on
liveness, contact and awareness events. The survivor-initiated row projection
stays exact. Coordinator-initiated recovery retains precisely the existing v2
normalization for its survivor; no additional row field is cleared. All current
eligibility checks still run at request and approval. A source that becomes
live cannot be recovered on an old consent.

## Version and proof boundaries

Both current recovery directions need a self-describing v3 projection, with
only the two cleared message fields above. General `MergeFence`, survivor v1,
and coordinator v1/v2 historical projections and literal fingerprints must
remain unchanged. Old pending requests require a new preview; no automatic
retry or consent is created.

A new required-reader feature precedes the first v3 record and survives
collection. Snapshot feature metadata must include it too. In particular,
rollback to 48773abc **after a v3 operation refuses to boot**, rather than
silently recomputing a legacy fence. No new state or operation fields are
needed.

Each cleared field has a permanent real-HTTP preview/request/approval guard,
on both protocol versions, both initiation directions and both boundaries.
The daemon change is a recorded operation, with setup assertions that its
marker changed. Contact tests capture an ordinary contact on an unread source
ask, fill its mailbox through admitted sends, then trigger a real capacity
refusal across the consent boundary. The writer derives its full-mailbox
escalation; the ask must name a new contact serial. Caller-supplied full-mailbox
evidence is deliberately ignored, and cannot drive this guard. Restart uses
the real encrypted ledger. The earlier synctest
draft stalled on real HTTP I/O preventing fake-time advancement and was
discarded; that was a fixture failure, not product evidence. Negative guards
must retain changed-mail, authority and eligibility refusals. Every new guard
must fail for its intended reason on old code before it can qualify the fix.
