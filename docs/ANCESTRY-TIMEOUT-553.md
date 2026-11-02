# Ancestry registration timeout: issue #553

Measured 2026-11-02 on macOS arm64, Go 1.27.3, detached source
`5ed8ab78236f56ca7555d3bd82a451eef22892d6`. The historical timeout did not
recur. Its cause remains unclassified; the passing measurements do not explain
it or establish which intervening change mattered.

| Capture | Cases | Actual exit | Wall time |
|---|---:|---:|---:|
| Unchanged fixture, `-count=20 -cpu=1,4` | 40 pass, 0 fail | 0 | 98.249 s |
| Timing overlay, same finite count | 40 pass, 0 fail | 0 | 93.690 s |
| Timing overlay, `-count=50 -cpu=1,4 -failfast` | 100 pass, 0 fail | 0 | 220.284 s |

The unchanged replies ranged from 0.721560 to 0.913655 seconds (median
0.763696), against the unchanged five-second assertion. The instrumented runs
are separate evidence: **this is not an unmodified 180-case failure rate**.
They added stage timestamps and aggregate stack-observer costs, which can
perturb scheduling. No synthetic host load, deadline, assertion or retry was
changed. A missing-import compilation failure executed zero cases and is
excluded from the counts.

Across the 140 instrumented cases, maximum registration time was 0.972005 s;
enrichment 0.768389 s; HTTP response headers 0.150529 s; daemon handler 0.147970 s;
and completed bridge write to parent receipt 0.038130 s. These maxima belong to
different cases and must not be summed. The largest individual observer stack
scan was 25.880 ms; the largest summed scan cost in one case was 239.168 ms.
These passing observations do not rule out observer overhead in an uncaptured
failure.

The original 1-in-40 event was instrumented source `2cd5790c`, before #552.
Its log is 108582 bytes, SHA-256
`f8ff23a5174aa245c4d7fc629f3f0f31a40403d2a1d4bf04b9713ccb158fe5b2`.
Both bridge lookups entered the held ancestry boundary, the parent reached its
five-second reply deadline, and the first subsequent lock-acquisition marker
was followed by the synthetic failure markers. The log contains no request,
HTTP or reply-stage timing that locates the delay.

The qualified commands used `go test -race -timeout=5m ./cmd/dibs
-run '^TestFailedAncestryRepairsSurfaceWithoutAnotherModelCall$'`, the counts
above and `-v`; instrumented runs additionally supplied a private Go overlay.
The larger capture retained its race test image, SHA-256
`c54cc426caf0018b558a0045794cbd176e630ee4816de3b39fdd6b0757b2c440`.
The exact logs, load samples, exit receipts, overlay/source hashes and per-case
events are retained with the implementation worker's diagnostic artifacts.
No capture in this table ran on the later #556 merge `0320b194`.

## Evidence the next failure must retain

- Parent/app/bridge fixture timestamps distinguish startup and request
  forwarding from work inside the bridge. The fixture's real HTTP wrapper
  distinguishes daemon entry and return; app and parent reply stamps locate
  later forwarding delays.
- A request delivered to the bridge without HTTP entry needs bridge stacks
  to distinguish enrichment or identity lookup from scheduling. Bridge
  internals are deliberately unstamped: production code gains no diagnostic
  hooks.
- HTTP entry without return needs daemon/parent stacks to distinguish writer
  queueing, storage, admission and other blocking work. HTTP return without
  parent receipt needs the bridge and app stacks.
- Attributing delay to the existing completion observer requires its scan or
  scheduler evidence overlapping a failure. Its presence alone proves nothing.

The fixture retains bounded first-boundary timestamps in shared memory and
dumps them with parent and private helper stacks only after a failed assertion.
It adds no success-path observer, scan, polling, logging or goroutine. The
existing completion observer and all assertions remain unchanged. Stack
collection terminates the already-failed private helper process group; it does
not retry the request or extend its five-second assertion. The issue stays open
for the failure dump to resolve the cause.
