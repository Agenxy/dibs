# Compact mail history: request 23228

Status: representation prototype only; full measurement and architect review
pending. No production history door or observer is implemented by PR #386.
This replaces the materialized representation accepted at bef678c, while
preserving its API, authorization, read-only semantics and page bounds.

## Measurement changes the budget

Architect 48322 retired the relative 10% bound after paired hosted replay of
the original million-record encrypted send/ack/sweep fixture left zero live
messages and only 319,560 bytes of coordination-only retained heap. Its 10%
allowance was 31,956 bytes. Sparse anchors/ranges added 12,976 bytes, while an
optimistic 32-byte-per-message authority-header floor added 10,683,856 bytes.
Neither arm was a complete history implementation.

The replacement budget is at most **48 incremental retained bytes per indexed
ledger record**, with **64 MiB per million records** as the hard ceiling.
Every allocation for history counts: snapshots, authority lookup, duplicated
party references, anchors, allocation slack and the mutable tail. Compare
paired production `Ledger.Replay` arms after forced GC on the same runner.
Report raw baseline/candidate heap and growth at 100k, 1M and 2M records.

## Representation proposed for measurement

Retain complete canonical audit-unit snapshots in immutable compressed memory
blocks, at most 128 units and 128 KiB before compression. Snapshots contain the
original proposal's serial tuple, operation time/kind, all mail/receipt/queue/
milestone metadata, author id/incarnation/host, content-presence flag,
before/after basis and eviction flag. Unknown-field labels are derived from
these values when rendering, exactly as in the prior proposal. Bodies,
responses, progress/review text, choices, milestone labels, attachment paths
and deliverables remain solely in the existing encrypted ledger.

The encoding is a representation of observed values. The observer captures
canonical before/after state and events; it does not reconstruct lifecycle
decisions from operation kinds. Same-op intermediate expiry/eviction units
retain the original ordinal and honest before-state basis. Bounded block
decode must reproduce every field exactly. A corrupt/unavailable derived view
fails closed with a corrective hint; ordinary coordination remains usable.

Maintain a sorted, chunked uint64 message-serial vector plus a chunked reference
to the latest canonical snapshot for each message, retained after GC. This is
the authorization directory: decode the latest header and call the shared
pure core authorization helper extracted from `GetMessage`, including the
creation fence and `State.AdoptedFor` rule. Never grant authority from a party
reference or cursor. A later ownership change affects permission for every
earlier unit. The snapshots already contain the full header; no second dense
header object is necessary.

Per-party/incarnation candidate vectors use chunked unit references. The probe
charges both copies, without relying on the fixture's unusually compressible
party ranges. Coalescing adjacent references into ranges may reduce this cost;
it must never widen authority. Adoption/merge must bring earlier candidate
units into the new party's enumeration while current-header checks remove
authority from stale references. The production design still needs a bounded
representation for inherited candidate unions; the simple fixture does not
validate this path or its cost.

Sparse ledger anchors retain serial, byte offset and the previous hash at most
4096 records apart. A content seek validates the chain against the next anchor
or committed head, then decrypts through `ledger.Box` after authorization.
Serial gaps must be supported; a line ordinal is not a serial. No offset,
body, participant identity or authority is accepted from the cursor. The
existing encrypted ledger remains the sole persistent store; there is no new
disk index or file containing decoded mail.

Reserve a bounded 128 KiB raw JSON tail for live commits. Published immutable
blocks and tail snapshots must be coherent with the writer's committed upper
serial. Replay and successful live append use the same canonical observer.
The measurement prototype seals its replay tail and includes the reserved
live buffer; production wiring and concurrency remain subject to review.

## Read and work bounds remain

Use `authObserve`, never `authRead`. Reading history cannot confirm socket
outcomes, mark delivery/ack, clear notices, change read prefixes or wake anyone.
Reauthenticate every stateless MCP 2026 page; the legacy surface uses the same
implementation. Preserve the original fixed-prefix cursor and serial tuple,
limit 1–100, 128 KiB encoded response, 4096 examined candidate units and 250 ms
budget checked between units. Latest authority is current even when returned
audit units are restricted to the cursor's older fixed prefix.

Decode outside the engine writer. Copy bounded immutable references and an
authority snapshot under the index lock, then release it before file reads.
Page-local decode caches and working buffers must be bounded and released;
they are not another retained history store. The implementation must measure
cold metadata/header lookups and authorized content seeks, and distinguish
the between-unit budget from a promise to interrupt a blocking file read.

The prototype uses uint32 references only below two million units. Production
must segment or widen them before overflow; it may never wrap or truncate.
Block byte limits must split early on actual raw length, not just unit count.

## What the prototype proves and leaves open

The full-contract arm first replays the exact encrypted ledger through the
unchanged production reader, then builds the representation using a second
canonical fold. Every block is decoded and compared with its input, and latest
headers are read back after GC. The full-field codec guard supplies nonzero
values for every accepted metadata/provenance field. The artifact changes no
production code and is not a candidate for merging.

The repeated two-party fixture measures this representation's retained cost,
not all workloads or the production history behavior. Adoption/merge,
incarnation reuse, many parties, rich metadata, many same-op siblings,
incompressible names, live tail reads and authority changes during pagination
need additional measurements/guards before implementation acceptance. Preserve
the original production-door guards, old-code RED proofs and full ordinary
gate. No tests, authority rule or retention limit may be weakened to fit.
