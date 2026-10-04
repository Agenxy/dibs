# Question withdrawal and notification cleanup

Request 21667 follows the installed request-withdrawal change (#325). A person
answered a question in another chat. Its sender needs to retract the unanswered
Dibs question without inventing an answer, and its notification should disappear.

## Replayable withdrawal

Keep `respond(disposition: "withdraw")` and `withdraw_message`. Extend sender
ownership to questions while preserving the creation fence and the rule that
adoption grants recipient access only. Preserve `SenderOwnsRequest` for its
existing request-specific callers; share the ownership fence below the typed
predicates rather than copying it. Questions may be withdrawn in pending,
delivered or acked state; answered, expired, displaced and already withdrawn
questions are final. Existing request eligibility stays unchanged.

Vocabulary and argument shape stay in Admit; ownership and current state stay
in the fold. Use the existing additive withdrawal fields, reason, retention,
recipient acknowledgement and event. Do not change old JSON tags or the
interpretation of historical response operations. An optional replacement must
be another sender-owned message of the same type; ordinary-request restrictions
remain. Withdrawal records `withdrawn`, never `answered` or `expired`, and does
not stop a process, start a replacement, or manufacture the person's answer.

Update withdrawal notices to name the message's actual type, including rebuilt
notices. Correct human delivery projection: `RespondedAt != 0` currently labels
a withdrawn request as answered. Message outcome and OS posting/cleanup evidence
must remain separate.

## Derived notification cleanup

Notifications are outside the fold. Trigger cleanup only after a successful
ledger append, through the common apply-and-ledger boundary, so web, MCP, desktop
and relay answers all use it. For messages to the durable human identity, an
answer/approval/denial/decline or sender withdrawal requests cleanup. Approval
is a decision even when work is still owed; no use of `Terminal()` alone may
mistake every queued work item for the same human decision.

Use a stable notification ID derived from the board node and message serial,
`dibs.msg.<node>.<serial>`. A serial alone collides when a Mac receives messages
from two boards. Both the local desktop and relay use the same typed identifier.
Pass it additively to the native posting helper through a dedicated environment
value; existing unkeyed banners retain their UUID behavior. Validate the value,
and never derive it from a sender's body or an arbitrary supplied identifier.

The bundled helper gains an exact removal mode for these IDs. It calls
`removeDeliveredNotifications(withIdentifiers:)`, never removal of all Dibs
notifications, and requests no new permission or foreground window. Apple's API
returns immediately and performs removal asynchronously; a successful helper
exit establishes that removal was requested, not that a person never saw the
banner. A bounded delivered-notification query can measure subsequent absence.
The receipt explicitly says best effort, retains historical posting evidence,
and reports unsupported/failed cleanup honestly. Linux and other unsupported
routes remain explicit; this follow-up does not pretend they have a macOS API.

An old helper ignores the additive posting ID and leaves an unidentifiable UUID.
It must not be reported as removed. A new removal response is structured and
self-describing, so an old helper's refusal cannot count as success. Do not
remove unidentified historical banners by title, body or global clearing.

## Ordering, relay Macs and restart

Removing an ID before a delayed post completes is insufficient. When a posting
receipt arrives, recheck the replayable message on the writer: if its decision
or withdrawal already happened, request cleanup again. Keep this bounded per
post/transition rather than introducing a polling watcher. A stale button's
answer still passes through normal state guards and cannot answer a withdrawn
question. Removing a banner need not fabricate dismissal or kill its bounded
waiting helper process.

The daemon cannot remove a notification on a relay Mac. Carry an additive
cleanup envelope on the existing authenticated relay feed, with the cleanup
serial nested and no top-level notice serial. Old relay readers then ignore it
instead of presenting it as a new question or blank banner. New relays handle
cleanup separately from their ordinary busy-notice suppression, remove on their
own Mac and report a distinct cleanup receipt. Apply the late-post check there
too. Validate receipt subjects through the existing human-message gate.

On restart/relay reconnect, derive bounded cleanup requests from retained human
question/request outcomes, capped at 64 in one batched helper invocation. Lost derived receipts remain unknown; rebuilding
cleanup never changes the ledger or invents delivery. Already garbage-collected
messages and unidentified pre-upgrade UUID notifications are explicit limits.
No new wake mechanism, autonomous answer or harness management is involved.

## Proof before shipping

- Real MCP question withdrawal must fail on parent #325, then pass for pending,
  delivered and acked questions across encrypted-ledger restart and recipient
  acknowledgement. Ownership, creation fence, final-state and replacement
  refusals leave state, serial and events unchanged; exact fold replay agrees.
- Real common-engine entry points prove human answers and withdrawal request
  cleanup, nonhuman mail does not, and failed/refused operations do not. A held
  posting fixture proves withdrawal-before-post is cleaned after the late post.
- Real relay stream/HTTP tests prove cleanup reaches each attached relay,
  reconnection derives retained cleanup, old readers ignore its envelope and
  cleanup does not forge an answer or overwrite posting evidence.
- Enter the public Go notification API and actual helper lookup. Negative
  mutations must fail the behavior tests; static call-site counts are no proof.
- Native signed-helper probe: post a uniquely scoped test notification, assert
  it is actually delivered, request its removal, then observe its absence while
  an unrelated test notification remains. If setup is not delivered, report a
  proof gap; do not count absence as successful removal. Clean up only probe IDs.
- Update SPEC, canonical/embedded SKILLS and CHANGELOG; run the unchanged full
  gate and exact-head hosted checks, obtain source review, then normal merge and
  clean real-Git install verification. No gate or outage assertion is weakened.

Apple API contract checked 2026-10-03:
[removeDeliveredNotifications](https://developer.apple.com/documentation/usernotifications/unusernotificationcenter/removedeliverednotifications(withidentifiers:)).
This is source evidence; the native probe remains required behavior evidence.
