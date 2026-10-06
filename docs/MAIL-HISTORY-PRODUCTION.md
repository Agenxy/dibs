# Mail history production checkpoint: request 23228

This branch is unfinished and must not merge. Request 54264 completed with
PR #390 at 2643b1d and PR #392 at 89b98f4. The accepted compact design (architect
48525) now proceeds on that actual main, after v0.0.13 publication.

The encrypted ledger remains the only persistent store. Production replay
captures canonical metadata before its existing Apply and observes it after
chain, decryption, fold and serial validation. Live commits capture before the
existing engine Apply and observe only after successful Append, following the
acceptance receipt. There is one fold, with one shared observer. Snapshot values
contain no mail body, response, participant paths, attachment paths or token.

The representation uses bounded compressed canonical snapshots, latest-header
references, both per-party reference vectors, sparse chain anchors and a bounded
raw live tail. Adoption/merge candidate prefixes point only to base vectors,
deduplicate by identity and freeze their lengths. A maximum of 256 sources
fails the derived view explicitly instead of dropping inherited candidates.
Latest shared core authorization will decide access; a prefix grants nothing.

Every production reference retains uint64 range. Chunks store low words and
allocate an upper-word plane only when needed; a behavioural guard crosses
both chunk boundaries and 32/64-bit values, then clears an upper word. The
compressor is reused with BestSpeed, and both its retained workspace and all
vector allocation slack count. The bounded derived format encodes primitive
values directly, preserving all metadata fields and timestamps, instead of
allocating and formatting JSON on the canonical writer. Only current mail
parties and the op's actor need their pre-fold identity captured. There is no
change to the ledger format and no second Apply.

The first actual production representation (b952d51) failed its bounds. Paired
hosted run 37531622245 measured these incremental retained-heap and replay
wall-time results; these are failures, not an accepted representation:

| Workload | Records | Bytes/record | Replay overhead |
|---|---:|---:|---:|
| Simple | 100k | 64.420640 | 72.757% |
| Simple | 1M | 51.788560 | 76.140% |
| Simple | 2M | 52.840072 | 72.286% |
| 50 parties | 1M | 55.669336 | 87.580% |
| Rich metadata | 1M | 54.352936 | 73.123% |

The adoption/merge fixture stopped on a no-op sweep before measurement and
now checks the target's status before recording its death. The new encoding,
reference planes and narrower identity capture must be measured afresh. The
prototype's earlier 30.8 B/record is not a result for this implementation.

The hosted paired probe generates one encrypted ledger per case. Separate
baseline and production processes enter the real Replay once, measure replay
wall time and forced-GC heap, and retain the actual complete representation.
Simple cases cover 100k, 1M and 2M records; 1M cases also cover 50 parties, rich
metadata and repeated adoption with merges. The probe checks every setup step,
prints raw values, and fails on more than 48 incremental B/record, more than
64 MiB per 1M records, or more than 15 percent replay overhead. A failure is
an unfinished implementation, never permission to relax a bound.

Still required: bounded stateless query and cursor, current creation/adoption
authority, chain-validated content seeks, no-consumption authObserve, real MCP
2026 and legacy doors, every tool-count spelling, SPEC and CHANGELOG, restart
and concurrency/privacy/read-marker guards, intended old-code RED, final full
gate, source review and coordinated merge. No local compilation/tests/load or
native delivery probes are authorized; all execution gates are hosted.
