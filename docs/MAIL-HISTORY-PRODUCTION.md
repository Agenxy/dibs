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

Production references are uint64 to avoid the prototype's uint32 overflow.
The compressor is reused with BestSpeed, and both its retained workspace and
all vector allocation slack count. These choices have not yet been measured;
the prototype's earlier 30.8 B/record is not a result for this implementation.

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
