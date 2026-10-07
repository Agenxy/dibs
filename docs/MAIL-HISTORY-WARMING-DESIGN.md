# Mail history warm-up: request 23228

Architect 56020 supersedes the native boot capture accepted in 55637. Answer
56033 explicitly authorizes a second canonical fold after serving, with the
private-state, canary, lifetime, peak and writer-latency conditions below.
PR #394 stays draft; the history query surface is not implemented yet.

## Measured reason

Synchronous encoding at 03b3474 retained 25.453952 B/record at 1M and
24.758176 at 2M but added 21.339%/21.733% to boot. Native field-delta capture
at 9ac0a98 also failed the accepted bounds. Unprofiled paired hosted probe
37539620262 measured 1M capture alone at +19.269548%, with the builder disabled.
The first HTTP reply added 18.942%, against 0.579% baseline spread. At 2M,
capture alone added 15.119292% and first reply 14.326%, with 1.161% spread.
Those first-reply times include engine boot and exclude the forced-GC pause;
they are not exact Accept timings.

Steady incremental heap was 25.541240 B/record at 1M and 24.853604 at 2M,
but sampled peak above steady was 132,039,576 and 266,681,648 bytes. The
allowed increments were 67,108,864 and 134,217,728 bytes. Capture is rejected,
even though its retained compressed index fits. A profiled repeat is a
diagnostic result, never a substitute acceptance measurement.

The first private-fold checkpoint 3efb598 was also not fully accepted. Hosted
37548603179 passed 0, 1M/2M simple, 1M many, rich and adoption/merge cases, but
100k simple failed peak: 11,552,880 bytes peak minus 4,551,232 steady gives
7,001,648 bytes, above the unchanged 6,710,886.4-byte allowance. Steady there
was 39.966640 incremental B/record. The reader now borrows in-buffer lines and
owns only oversized lines; its allocation effect must be measured afresh.

At 3efb598, 1M/2M steady increments were 26.083480/25.246108 B/record. Peak
above steady was 25,747,224/47,485,336 bytes, both within the 64/128 MiB
allowances including shadow. Warm-up took 8.937024/15.420722 seconds; real
writer p99 was 2.635831/0.626879 ms with 3,984/7,709 samples. Boot Replay alone
was 5.962309/11.222768 seconds versus 6.064754/11.207122 for the first baseline.
First-reply comparisons were -6.914%/-7.719%, with 6.898%/7.403% control spread;
these are passing bounds, not evidence of a speed improvement. ALL3 at 3efb598
failed Mac lint (complexity, line lengths, exported comments), corrected in
source without changing thresholds. The replacement source is unmeasured.

## Boot boundary and private canonical fold

Replay retains the existing single Apply, event reconstruction, chain and
serial validation. It performs no per-record history capture, projection or
encoding. At successful replay completion it freezes S0, the committed byte
boundary, chain head, record count, node ID and actual Limits. This constant
boundary precedes engine boot operations: all subsequent committed operations,
including those before Accept, enter the live ordered queue.

The actual net/http listener's first Accept starts the cancellable reader.
It uses SectionReader/ReadAt on the same committed file, never changing the
writer's file offset, and reads only bytes through S0. It verifies the chain,
decrypts each op, captures bounded canonical before metadata, and calls
core.Apply with the ledgered time and the same Limits. Its State is newly
constructed, shares no pointers with live State, and is never retained in the
Index or read by a request path. No transition rules are copied out of core.

Each canonical before/after pair and fold events enter the same projector as
the live observer. Boot units encode directly into bounded independent codec
blocks, with no backlog of raw boot snapshots. Only one ledger line and the
current codec block are processed at a time, beside the bounded canonical
shadow State and its transient metadata scratch. The reader yields every 256
records. Actual warming wall time and writer p99 decide whether that suffices.

The final serial, committed byte boundary and chain hash must match S0. A
canonical State JSON hash (with core's existing credential redactions),
computed in the background, is the regression
canary: tests compare it with the actual live board at S0 using nondefault
Limits and changed live objects. No shadow State or scratch escapes the reader;
both are dropped at S0. Later records use only the live observer, without
another Apply.

## Live ordering and failure

Live metadata deltas use a fixed-segment FIFO capped at 64 MiB, including the
active chunk and records containing only sparse anchors. The builder drains
them only after S0. Queue saturation fails the derived view rather than blocking
the writer or presenting omitted units as complete. Sparse anchors are ordered
by the builder, including records with no changed mail. Compression holds a
view lock separate from the live capture lock.

Architect 57773 refines readiness: E_HISTORY_WARMING applies only until the
initial boot reader reaches S0, once per daemon start. Steady queries copy the
request-time board serial and wait at most 250 ms for the live consumer. If it
is still behind, they answer through the drained serial with explicit
as_of_serial and behind_by. Live writes may temporarily clear the internal
Ready flag; that must never turn a steady query back into WARMING. Current
creation/adoption authority and authObserve remain mandatory: history cannot
consume mail, outcomes, reviews or socket offers. Concurrent steady writer/query
guards must prove no WARMING errors and an as_of_serial on every response.

## Required proof

Enter through production Open/Replay and the real serving transition. The
boot-deferral guard asserts no captured units, queue bytes, encoded units or
builder before serving. The private-state canary changes live Message and
Agent objects and commits through the real engine before Accept, proving the
reader uses independent state and the live suffix follows S0.

Keep paired same-runner baseline controls at 1M/2M and the <=15% boot-to-serving
bound. Forced-GC steady incremental heap stays <=48 B/record. Sampled warming
peak, INCLUDING shadow State, stays <=steady plus 64 MiB per million records.
Report warming wall time and real coordination-operation p99. A failed shadow
heap bound is a measured failure to report, never permission to relax it.

Still required: real MCP warming-to-full-history proof, complete stateless
query/content/cursor/authObserve implementation and its privacy/read-marker
guards, cancellation/restart/queue-saturation proofs, every intended old-code
RED, source approval, full gate and coordinated actual merge. No local
compilation/tests/load or native delivery probes are authorized.
