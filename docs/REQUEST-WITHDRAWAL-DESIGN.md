# Sender withdrawal of requests

Proposal for request #20582; review before implementation. Based on main
`2a3c95ec169c2dbb4612956593884b392a46d114`.

The reassignment of #11960 required its former recipient to report `done`
while saying the work was not implemented. The completion notice then asserted
delivery. Withdrawal must close the obligation without making that assertion.

## Operation and authority

Expose `respond(token, msg_serial, disposition: "withdraw", body: reason?,
superseded_by?)`, backed by the new `OpWithdrawMessage` kind. A separate operation keeps sender authority out of
the recipient response path, including its approval/grant/adoption guards.
The engine resolves the surface disposition before admission, keeping the
recipient response guards out of the ledger operation. Architect #21066
accepted this surface refinement to preserve the unchanged tool-list budget.
Both MCP 2026 and the legacy tools surface call the same engine operation.

The authenticated sender may withdraw an ordinary request in `pending`,
`delivered`, `queued` or `approved`. An unapproved grant/adoption request may
also be withdrawn; an approved one has already performed its effect and is
refused. Done, denied, declined, expired, displaced and other finished states
are refused with `E_MSG_FINAL` and a hint to send a new request. There is no
admin override, recipient withdrawal or implicit reassignment.

Ownership is `message.From == actor.ID`, with the same creation-serial fence
as `GetMessage`: a reused identity cannot withdraw its predecessor's outgoing
mail. Adoption transfers recipient authority, not sender authority. A nonce
resume keeps creation serial and therefore keeps sender authority. Modern
purges retire `From` and also fail this check. Historical rows with zero
creation serial retain the existing no-fence convention.

An explicit merge rewrites `From`. The survivor can withdraw rewritten mail
that passes the creation fence, just as it can currently read it. Mail older
than the survivor remains refused by that existing fence; this proposal does
not invent sender adoption provenance or loosen that privacy boundary. This
limitation is explicit for review rather than hidden behind ID equality.

`superseded_by` is an optional existing, different ordinary request sent by
the same authenticated sender and passing the same fence. It may address a
different recipient (reassignment). It is a reference, not an operation on
the replacement: no send, approval, cancellation or queue insertion occurs.
Unknown, self and someone else's references are refused without disclosing
their content. The replacement need not be unfinished; the reference records
what superseded this request, not a promise of its current status.

## Admission and deterministic fold

`core.Admit` validates the new vocabulary and field shapes: required nonzero
message serial; bounded optional reason using the existing body limit; optional
nonzero replacement serial distinct from the withdrawn serial; withdrawal
fields forbidden on unrelated operations and recipient-only report fields
refused on withdrawal. MCP parses the optional serial
strictly rather than dropping malformed values. No old JSON tag is renamed.

The new fold branch checks ownership, creation serial, message type, state,
and the replacement reference before any mutation. These are decisions about
state on a new operation, not retroactive checks on old `respond` or `send`
operations. Existing ledgers replay unchanged. A successful withdrawal calls
`finish` exactly once; refusal changes nothing and advances no serial.

Record `state: withdrawn`, withdrawal reason and optional replacement on the
message, plus the withdrawing actor and withdrawal serial/time. Use additive,
explicit fields rather than overwriting the recipient's approval response,
progress or deliverable. `RespondedAt`/`TerminalAt` identify this new terminal
transition; the original progress remains evidence, not a completion claim.

Clear `QueueDebt`, rank and task-scoped ordering lock; retain prior priority
metadata as history. Recompute remaining queue ranks deterministically and
emit the existing queue-change receipts. A queue lock protects ordering,
not execution or the sender's ability to retract its request. There is no
waiting for a recipient and no change to its declarations or running process.

## Receipts, retention and restart

Return `{ok:true, state:"withdrawn", msg_serial, superseded_by?}` to the
sender. This is its receipt, so no second sender verdict wake is needed.
Emit `message.withdrawn` from the sender to the current recipient, naming
the request and optional replacement; wording says the sender withdrew it,
never that work was delivered and never that the recipient must stop.

Mark the withdrawn envelope unconsumed for the recipient. Its terminal inbox
entry carries the withdrawal fields; existing `ack(msg_serial)` consumes it.
Reading it may clear the current notice as today, while ack provides the
durable receipt. Rebuild the recipient's withdrawal notice from unconsumed
message state after restart, with the recorded age and actor. Do not route it
through the ordinary sender-verdict rebuild, whose recipient and awareness
watermark point in the opposite direction. The sender's own outcome watermark
must not substitute for the recipient's receipt.

Add withdrawn to `Terminal`; `Owed`/`DurableDebt`, task queue, outstanding-work
and continuation projections exclude it. Keep unconsumed terminal retention
and tracked-task TTL semantics, including their existing capacity bounds.
Use the existing recorded retention-decision mechanism where needed; do not
extend the old response fold retrospectively. Replaying and rebuilding must
not restore queue debt or a notice already acknowledged.

## MCP tasks and human board

Map a withdrawn tracked request to `cancelled` with a factual status message
and a result carrying `state: withdrawn` and its replacement reference. Task
subscriptions already stop following any non-working snapshot; verify the
withdrawal event actually drives that snapshot and ends the stream.

Keep `tasks/cancel` as its current cooperative acknowledgement. The task handle
is a bearer capability, while withdrawal requires the sender's agent token;
silently making that handle sender authority would expand its contract. Update
the explanation to distinguish cancellation acknowledgement from the explicit
sender withdrawal disposition. No agent process is stopped by either.

The board shows a neutral `withdrawn` tag, reason and replacement link, with
no delivered/completed wording or approve/done actions for that envelope.
`read_mail.outstanding` says withdrawn rather than owed. Change SPEC, canonical
SKILLS and its embedded copy, and CHANGELOG together. The tool count stays unchanged.

## Proof before shipping

Start with real MCP-door regressions that fail on this parent: queued and
approved withdrawal, pending-before-delivery, truthful sender/recipient
receipts, queue-rank/lock cleanup, owed/outstanding/continuation removal, and
late recipient progress/done refusal. Exercise ownership, reused-ID and
adoption fences, replacement validation and already-effectful approvals.

Drive encrypted-ledger restart before receipt and after ack: the same request
remains withdrawn, no debt returns, and only the unacknowledged recipient gets
its receipt. Check tracked tasks through get and a live subscription, and
render the board through its real route. Compare replay state/serial with live
state; rejected calls must append no operation. Run the unchanged full gate,
independent review, guarded merge, clean real-git clone install, normal upgrade
and signature/version/replay verification. None of that is claimed by this note.
