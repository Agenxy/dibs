# Ancestry registration timeout: issue #553

Classified 2026-11-06 as a **test fixture defect**: the completion observer
could stop its own bridge for an all-goroutine stack scan before registration
had reached HTTP. The repair orders that observer after the parent has read
and validated the real reply. The original five-second registration assertion
and the requirement to observe the real completion worker waiting on ancestry
remain enforced. Production bridge code is unchanged.

## Captured failure and matched repair

On production source `2cd5790c178cc9e0d9e8d1fb46748eb956223548`, a private
overlay backported #561's test diagnostics and #577's fixture cleanup repair.
Only six test files were overlaid; the #552 unlock changes were absent. A
200-case CPU1 run reproduced the registration timeout in case 191: 199 passed,
one failed, actual Go exit 1, and one failure dump was captured.

The dump's first boundary stamps locate the request:

| Boundary | Elapsed from evidence setup |
|---|---:|
| Parent wrote request | 1.313290 s |
| App-shaped helper forwarded request to active bridge | 1.339047 s |
| Active bridge entered the held ancestry lookup | 1.358246 s |
| Parent collected evidence after its five-second reply failure | 6.313628 s |

No HTTP-handler entry or registration-reply stamp was present. The active
bridge's request goroutine was runnable in JSON enrichment. Its test observer
was **stopping the world**, in `observeSurfaceCompletionWaiter` →
`runtime.Stack(all=true)` → `runtime.stopTheWorld`; the synthetic ancestry
lookup remained held in `flock`. The JSON frame is the suspended request's
location, not evidence of a slow JSON decoder. The dump identifies a pre-HTTP
observer pause; the duration of one individual stack scan was not measured.
The raw case-191 dump has SHA-256
`012ca914137dff58927548a3cd3f1fc41ef715294f37321219097248e389bd9d`.

A separate private six-second handler stall first verified the capture path:
the unchanged five-second assertion failed, with handler entry but no return,
parent/app/bridge stacks, and a blocked bridge HTTP round trip. That deliberate
stall was excluded from every stress overlay and from the counts below.

The repair is also the causal discriminator. The parent writes `gate.replied`
only after reading and validating the actual registration response. The child
observer waits for that marker before any stack scan, then still must identify
the actual `surfaceCompletion.advance` waiter before the parent releases
ancestry. A shared evidence stamp records the first scan; the real contract
asserts that it follows the parent's reply receipt.

| Validation | Actual result |
|---|---|
| Ordering guard on main `de64859b`, before repair | Intended RED: scan before real reply; Go exit 1, 3.060 s |
| Same guard on instrumented old `2cd5790c`, before repair | Intended RED: scan before real reply; Go exit 1, 2.638 s |
| Repaired main, both failure contracts, `-count=20 -cpu=1,4 -race` | 80 pass; Go exit 0, 169.735 s |
| Repaired test on the same old `2cd5790c` production bytes, CPU1 ordinary | 800 pass in four 200-case batches; every Go exit 0; zero reply timeouts or dumps |

The matched 800-case run's maximum completed reply was 1.752827542 seconds.
No synthetic host load was added; measured one-minute host load ranged from
6.14 to 16.06. Each batch verified the immutable overlay manifest and six
test-file hashes, and source stayed at the exact clean old pin. The repaired
overlay retained the old fixture's close-only lock release: it did not borrow
#552 to make this result pass. This matched result, together with the captured
pause and the ordering guard, classifies the reproduced reply timeout as a
fixture defect rather than a production registration stall.

**Current main still needed the repair.** Main `de64859b` launched the same
stack observer before registration; its old-order guard failed even though
the reply arrived in 828.166583 ms. Neither #552 nor #577 removed that scan.
An earlier unmodified run of this main passed all 800 cases across CPU1/4 and
ordinary/moderate load, but those passing samples alone did not classify the
failure. The original 79 passes followed by a diagnostic mapping crash are
separate evidence; #577 repaired that cleanup fault.

The historical unmodified comparison also remains separate: pre-#552
`2cd5790c` passed 798/800, with one reply timeout and one post-reply surface
failure; post-#552 `51d4370d` passed 800/800. Those pins also differ in local
IPC publication code, so that comparison cannot isolate #552. The matched
old-source repair above removes that confound for the reply-timeout diagnosis.

## Earlier 2026-11-02 measurements

Measured 2026-11-02 on macOS arm64, Go 1.27.3, detached source
`5ed8ab78236f56ca7555d3bd82a451eef22892d6`. The historical timeout did not
recur in those measurements. Its cause was then unclassified; these passing
measurements did not explain it or establish which intervening change mattered.

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

## Failure capture contract

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
The completion observer runs only after real reply receipt, and its first scan
stamp guards that ordering. Stack collection on failure terminates the
already-failed private helper process group; it does not retry the request or
extend its five-second assertion.
