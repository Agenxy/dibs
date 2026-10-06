# Mail history warm-up: request 23228, architect 55438

This is a proposed capture layout, not a completed implementation. PR #394
stays draft. The architecture decision in 55438 supersedes further attempts
to fit synchronous encoding into the boot budget.

## Measured reason

Same-runner controls at 1c34fc7 measured 0.5–1.4% baseline timing spread at
1M/2M while production added 18–28%, a real failure. The last synchronous
encoding checkpoint 03b3474 reduced retained heap to 25.453952 B/record at
1M and 24.758176 B/record at 2M, but boot remained 21.339%/21.733% slower
(controls spread 0.907%/1.391%). It is not an accepted boot result.

## One canonical fold, native capture

Keep the existing single Apply. Before/after canonical metadata and fold
events supply every captured fact, including silent reads, ownership changes
and same-op eviction siblings. Neither the capture nor the builder applies
an op or reconstructs transition rules from op kinds.

A full native snapshot contains five metadata timestamps, author, strings
and counters: its field layout is roughly 432 bytes before string backing
allocations (a source layout estimate, not a hosted allocation measurement).
One million such snapshots would exceed the warm-up peak envelope. Capturing
full snapshots and promising to compress them later cannot satisfy the bound.

Proposed raw layout: independently owned chunks with fixed-width rows and
scalar columns, containing canonical field changes, not serialized snapshots.

| Data | Native representation |
|---|---|
| Row | field mask, uint64-column start, uint32-column start |
| Wide values | uint64 serials/counters and signed-int bit patterns |
| Narrow values | uint32 ordinal/flags and chunk-local string/time references |
| Strings | native immutable string table, no bodies or paths |
| Times | native time.Time table, retaining canonical time values |

Each chunk begins with a full set of field values. Later rows store only
changed primitive fields. A scalar's type and order are fixed by the schema;
there is no JSON, varint, timestamp serialization or compression on replay.
Tables and scalar storage are segmented, avoiding geometric doubling. The
capture interning maps are dropped when sealing a chunk; completed chunks
are freed after the builder consumes them. Only the active chunk keeps maps.
All allocation slack, raw arrays, tables and retained compressor workspace
count toward the hosted peak and steady-state measurements.

The point needing clarification is whether these native field deltas satisfy
55438's “raw fixed-width tuples” and “no snapshot encoding.” Treating full
Go snapshots as that tuple does not fit the peak envelope.

## Serving and live ordering

The daemon's real serving transition starts the cancellable background
builder. It is not started from EndReplay, and no caller waits for compression
to finish before the board can serve. The same builder consumes live commit
captures in canonical order. It cannot block the writer on a full queue: an
explicit failed derived view preserves ordinary coordination and produces an
honest history error. An omitted unit must never become apparent completeness.

History queries remain E_HISTORY_WARMING until every captured committed unit
and the corresponding watermark have been built. The hint gives progress and
the corrective mail_history retry. No partial view is returned as complete.
After catching up, the existing stateless prefix/cursor and latest-header
authority contracts apply. Authentication remains authObserve; warm-up may
not consume live mail, outcomes, reviews or socket offers.

## Required proof

Enter through production Replay and the daemon serving transition. Preserve
the same-runner baseline controls and measure replay-to-serving overhead at
1M/2M, requiring at most 15%. Measure warm-up wall time and sampled peak heap,
plus forced-GC steady heap. Steady heap stays at most 48 B/record and peak
stays at most steady heap plus 64 MiB per 1M records.

Run a real coordination-op latency probe during warm-up and report p99. A
real MCP query during warm-up must fail with E_HISTORY_WARMING, then return
the full correct history after warm-up. Test live appends during building,
ordering, cancellation, queue saturation, replay/restart and read-marker
preservation. Retain all privacy/content/cursor guards and intended old-code
RED. If native capture itself misses 15%, report it rather than adding another
fold or weakening a bound.
