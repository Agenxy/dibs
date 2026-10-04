# Native queue admission: measured gaps and limits

Request 26042 reports empty Codex activations after old queued notices drain.
The October 3 receiver transcript records thirteen successive notice turns
from 23:48 to 23:58 Pacific. The cited 18:48:54/56 pair is one command
acceptance followed by a deferred app-opening log, not two queue acceptances.
An unloaded/away opening only exposes the retained queue; it does not enqueue.

On October 4, the installed `codex-cli 0.160.0` read-only API showed three
pending items in the reported worker's queue: notify, question and answered.
Another worker had answered followed by handoff. The production observer
recognized these mixed queues in 45–65 ms. No diagnostic added, deleted,
resumed or loaded a thread. A read-only storage snapshot corroborated item
creation times; product code continues to use the API only.

Three current gaps reproduce through the production command door:

| Gap | Parent failure | Corrected behavior |
|---|---|---|
| The observer's private vocabulary omitted verdicts | A pending answered, approved, denied, declined, done, withdrawn or adopted item was followed by a second handoff item | Read canonical verdict kinds from `core.IsMailEvent`; the existing item coalesces the handoff |
| Admission used a process-local mutex | Two independent processes both observed empty and committed two items | A private per-thread OS lock covers observe, enqueue and receipt retention |
| A fallback called the bare executor | Two primary refusals sent two queue fallback items | The fallback enters the same queue admission door after the primary releases its admission |

The executable fixture now records the actual `--message` argument. It had
always stored a question, hiding the verdict bug. Its independent queue writers
commit transactions under a separate storage lock, so the concurrency test
measures duplicate admission rather than a fixture's lost update. The first
writer is paused inside its actual command while a second test process enters
`RunCommands`; after release, exactly one item remains. No goroutine stands in
for a second process. Consumption still re-arms the next notice.

All three tests fail against unchanged production at `c39e538`, with the
corrected fixture. The question control passes there. They pass with the fix.
The historical logs did not record whether each individual observer was
available or which inferred receipt cause re-armed it. These tests establish
present gaps, not a retrospective claim that one gap caused all thirteen turns.

## The explicit recovery trade

Architect answer 29103 retains the existing unavailable-observer policy.
When observation is unavailable, an authenticated current prompt, a new app
reconnect generation or the two-hour receipt expiry can each admit at most
one additional notice. A possible empty activation is preferred to losing a
wake. When observation succeeds, an actual pending notice wins over all those
inferred causes, and actual consumption re-arms immediately.

Every upgraded writer using the same board receipt directory takes the OS
lock. It remains on disk, because unlinking a held lock would create two
independent inodes; process exit releases ownership. Acquisition is bounded
by the command timeout, and failure sends nothing and reports failure. A
still-running old writer does not acquire this new lock, so the guarantee
requires both writers to upgrade. No payload or capability contract changes.

The fix adds no queue-deletion operation and cannot remove a pre-existing
backlog. It preserves app ownership and the operator's away-opening policy,
uses no session-hosting command, changes no core operation or ledger field,
and starts no background event poller.
