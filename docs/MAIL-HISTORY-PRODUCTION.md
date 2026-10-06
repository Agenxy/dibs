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

The next resource revision reuses writer-owned transient capture and sorting
workspace instead of allocating it on every op. Every immutable snapshot is
encoded before reuse. Compression blocks admit up to 1024 units but still
split on the same 128 KiB raw byte ceiling; both split conditions are tested.
Native previous-hash bytes come from the actual chain so a record cannot use
a stale anchor after a failed decode. The baseline skips all history record
bookkeeping, not only Observe; earlier baseline arms included that small common
cost, so their overhead figures are historical measurements of that arm.

Same-runner controls at 1c34fc7 (run 37533666507) put baseline time spread at
1.411% for simple 1M, 0.485% for simple 2M, 0.714% for 50 parties, 0.821%
for rich metadata and 0.639% for adoption/merge. Actual overhead remained
26.032%, 27.871%, 20.216%, 18.030% and 17.771% respectively: a real failure,
not runner noise. Memory now passes at 100k (46.243280 B/record), 1M simple
(33.476800), 2M simple (32.771120), 50 parties (37.665224), rich metadata
(35.912248) and adoption/merge (25.153776).

The next disposable codec format encodes a full first snapshot in each block
and only changed primitive fields afterwards. Blocks remain independently
decodable and retain the same unit/byte caps. A byte split re-encodes the next
unit in full before appending it to a new block. A behavioural guard changes
every snapshot leaf separately and reverts it, so a forgotten future field
fails the real codec rather than hiding behind a manually maintained literal.
No authority, cursor or ledger field depends on this internal format.

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
64 MiB per 1M records, or more than 15 percent replay overhead at 1M/2M. At
100k the absolute time delta and percentage are reported together, because
fixed setup costs dominate; architect 55242 requires two same-runner baseline
repeats to measure their spread first. All three baseline times and their
spread are reported. The acceptance comparison keeps the first baseline;
no favourable control is selected to make the overhead smaller. A failure is
an unfinished implementation, never permission to relax a bound.

Architect 55350 also requires the actual empty-ledger increment and growth
slope. A zero-record case now enters the same Replay door. Its codec is lazy,
so zero-record heap does not include the compressor and raw tail allocated
after the first mail unit; that distinction must accompany the number. The
100k memory ceiling stays in force until a measured fixed ceiling and slope
are explicitly reviewed; it has not been replaced with a fitted estimate.

Still required: bounded stateless query and cursor, current creation/adoption
authority, chain-validated content seeks, no-consumption authObserve, real MCP
2026 and legacy doors, every tool-count spelling, SPEC and CHANGELOG, restart
and concurrency/privacy/read-marker guards, intended old-code RED, final full
gate, source review and coordinated merge. No local compilation/tests/load or
native delivery probes are authorized; all execution gates are hosted.
