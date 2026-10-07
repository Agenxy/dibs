# Mail-history query concurrency checkpoint

Request 23228. API contract accepted at bef678c/29830, steady-state refinement
57773. Architect 57905 accepted these phases with the scalar fence below
instead of the proposed ownership overlay. The first API source is wired;
runtime verification and the remaining discriminating guards are still owed.
Foundation e771 passed ALL3, all seven
resource cases and all six revised latency cases at 95823d3/run37558075241.
Original 56794 failures remain failed. The revised latency criterion is
explicitly looser during warming, per owner 57658.

## Query phases

1. On the engine writer, authenticate/rate-limit with authObserve. Copy the
   caller's immutable identity and CreatedSerial plus the request-time board
   serial. Never keep a pointer into live core state.
2. Outside the writer, wait within the one 250 ms request budget for the
   derived consumer. WARMING describes only an unfinished initial S0 fold.
   Once S0 has finished, use the drained prefix even if new live deltas remain.
   Always return as_of_serial and behind_by relative to that copied board serial.
3. Under a short view lock, copy at most 4096 candidate references from the
   caller's own incarnation and frozen inherited prefixes. Copy immutable
   compressed block descriptors and the bounded raw tail; decode outside all
   writer/capture locks. The latest ownership header can be in another block.
   Decode one bounded block at a time, not a cache proportional to candidates.
4. On the writer, reauthenticate the current token/incarnation and check that
   the ownership fence has not advanced since admission. Current live Message
   headers override historical headers. Apply core.MessageAccess
   to metadata and content alike; no coordinator/admin bypass.
5. Outside the writer, seek/decrypt eligible content only, with chain-validated
   anchors and bounded input. Never read attachments or fileref paths. Before
   returning, reauthorize token/incarnation and ownership again on the writer.
   Serialize at most 100 rows/128 KiB with a stateless fixed-prefix cursor.

The current helper uses the conversation lookup serial explicitly, preserving
GetMessage's existing behavior even for legacy/test Messages with an empty
embedded Serial. It shares adoptedForReader with State.AdoptedFor; the rule
does not depend on a second copy of adoption semantics.

## Authority when the consumer is behind

The latest compressed header alone is insufficient: an adoption may commit,
then GC may remove the live Message, before the consumer reaches that delta.
Authorizing with the older compressed recipient would reveal the moved mail.
Waiting for the request serial does not solve a move during content I/O.

Use one derived writer scalar, lastOwnershipChangeSerial, per 57905. After a
successful append and before publish, compare canonical before/after ownership
headers; update the scalar if any message's From, To, AdoptedFrom or AdoptedAt
changes. This covers adoption, merge and future ownership-moving operations
without a hand-maintained op-kind list. Birth and ordinary receipt/read/GC
changes do not move the fence. No op/tag/core-state or second store is added.

At admission, copy that fence. Wait within the same 250 ms budget for the
consumer to reach it. If it remains behind, refuse with E_HISTORY_SETTLING,
a corrective retry hint and the lag, instead of trusting stale authority.
At final reauthorization, any advance of the fence since admission discards
the result and returns the same error. This deliberately refuses around a
rare ownership move, even one in another conversation. Ordinary writes still
answer through the drained prefix with as_of_serial and behind_by; they never
flap into WARMING. The boot fold already reconstructs pre-S0 ownership, so
the derived fence starts at zero and observes committed live changes only.

The overlay proposal is rejected, and its separate heap gate is withdrawn
because no per-conversation ownership structure will be added. Existing full
resource and latency gates remain mandatory for the finished implementation.

Required discrimination: hold the consumer through a real fixture boundary;
adopt a pending message, evict it by a real accepted operation, and query as
the old recipient while the index still has the old header. The old recipient
must receive neither metadata nor content. Also move ownership during the
real content read, proving the final reauthorization rather than a setter.
Concurrent busy writer/query cases must remain out of WARMING and include
as_of_serial even when reporting behind_by. Every new behavioral guard must
fail through the same production door on old source.

Byte-based sparse anchors (architect 58315) also bound valid large-body intervals:
the first post-S0 commit anchors, then either 4096 records or 4 MiB of native
offset emits the next anchor. Bootstrap owns independent offset scratch. A single
oversized legacy record remains explicitly unavailable. The native small-body
regression must pass in both live capture and rebuilt bootstrap and fail on
c241fe3; a 100k-record 32 KiB-send resource case keeps the original 48 B/record
retained ceiling and 64 MiB/million warming peak ceiling unchanged. Runtime
proof and the updated resource matrix remain owed.

The100k large-body job at3e80/run37565305007 failed the checker as written:
47.6972B/record retained passed, but13,413,656B peak less5,335,312B steady
was8,078,344B above steady, beyond its proportional6,710,886B allowance.
Architect58415 directed applying the fixed8MiB working-set floor he attributes
to56771: the accepted warm allowance is max(64MiB per million records,8MiB).
The earlier56771 message is no longer accessible via read_mail;58415 is the
current explicit authority and rationale. The original run remains failed.
The checker now emits both the proportional verdict and accepted max/floor
verdict; the48B/record retained limit stays unchanged. The new exact-head
resource gate must still run; exceeding the floor at larger N remains a finding.

Actual native port receipt at240e99/run37565955823 authenticated a4,165,856B
span under-race for250.735644ms, then returned context deadline exceeded.
The anchor was cut and found; this was budget exhaustion, not bad evidence.
Architect58458 approved a tighter512KiB target while retaining the250ms
query budget and16MiB/4096-record hard reader limit. Denser anchors must be
charged by all8 resources. Non-race span timing remains unmeasured and owed;
-race's extra cost is not a general constant-tuning yardstick.

At771/run37566315360 the live and rebuilt512KiB-target content spans were
489,519B, authenticated under-race in24.948415/25.015509ms without error.
The MCP candidate budget guard passed; the native candidate fixture still
failed setup because it replayed with ConsumedRetention=0 while its generator
used the original default limits. That deleted approvals in the recorded dead
sweep before adoption. The fixture is corrected to the same original limits;
this setup failure remains invalid as a product or old-control verdict.

Hosted1dc/run37566582265 passed16 real-door baseline tests at359ba2f,
six pre-API absence guards, the c241 large-interval guard and10 intended
runtime failures across8 removed safeguards. The original240 4MiB target
without-race authenticated4,165,850B in108.548746/53.301322ms (live/rebuild).
These were measured on the same hosted proof runner, not a claimed universal
ratio to the earlier-race refusal.

Architect58511 accepted three separate allocation stages: fixed128-anchor
segments (689f3b4), empty codec tail/scratch release after a drained flush
(0f3508e), and a64KiB private parser aligned with the native read bound
(c0f47f1). Metadata remains immutable after flush and no canonical fields or
permission rules change. An isolated hosted sequence must measure each stage
on identical encrypted input; final8 resources and revised writer latency
must pass afresh.359 resources passed8/8 but byte-identical production771
failed49.38408B/record and8,439,504B above steady, so a single near-bound green
is not evidence of comfortable margin.
