# Paired writer-latency cause measurement (disposable, never merge)

ACTIVE priority57442: candidate productionf1d8d3c is tested with the SAME
shared disk costs and bound below, at1M/2M paced and1M back-to-back. Both
Append exits are checked through real Open/Replay/Engine.Do and native
Write/Sync. A private atomic observer interface allows identical guards to
run against olda709 without referring to missing fields at compilation;
old proof must fail both intended assertions AFTER actual operation setup.
The native file port is now part of production; fixed-source tests need no
file-field overlay. Old source gets only the prior two file seams widened.
The builder must finish under the original five-minute context with back-to-
back writer calls. Wall time, writer device queueing and largest native read
are reported; actual reads over64KiB fail. No costs/bounds are retuned.

Corrected coordinatorfc1416d/run37554335782 reproduced the synthetic contention
at both sizes, with all frozen-prefix, real operation, ready and queue checks
passing before the intended latency assertions failed:

| Records | Control p99 ms | Warm p99 ms | Bound ms | Warm seconds | Writer device queue seconds |
|---|---:|---:|---:|---:|---:|
| 1M | 15.872921 | 66.158660 | 31.745842 | 19.807705 | 11.891144 |
| 2M | 16.518606 | 68.178105 | 33.037212 | 42.112037 | 23.459277 |

Both no-warm device queues were0. ReadAt239/480 operations charged exactly
250,518,824/502,518,931 frozen-prefix bytes. This proves the model's contention
RED, not the original ended runner's cause. The current candidate yields while
Append is active, at most5ms before each64KiB read and256-record fold chunk.
At20MiB/s a64KiB read occupies3.125ms plus0.5ms fixed cost, small against the
~30ms bound; in-flight reads cannot be recalled. No local execution or tuning
of the acceptance thresholds occurred. Fresh original six-run acceptance is
still owed after actual model GREEN, not replaced by this diagnostic.

Historical priority57332 superseded the unrun fixed-sleep proposal57287. One
test-only disk service budget is shared by actual builder ReadAt, writer Write
and writer Sync on each of two fresh hosted runners (1M/2M). It has no burst:
FIFO reservations cost bytes/20MiB/s plus 0.5ms per read/write and 14ms per
flush. No-warm should land near 15ms. Identical disk costs in both arms; no
artificial workload, writer failure or index lock is introduced. A test-only
file wrapper reserves capacity then calls the actual native operation.

The coordinator widens ONLY the ledger file and bootstrap ReaderAt fields
in a disposable hosted overlay. The wrapper is installed before real Replay,
so configureHistory captures that same file and first serving Accept starts
the actual builder; no manual bootstrap wiring or warming flag. Byte-exact
overlay checks, restoration and final clean tracked diff verify the seam.
Per-op counters assert each timed Do entered native Write and Sync once;
zero reads before serving and exact frozen-prefix read bytes prove the reader
uses the shared disk. Same process/fixture, original per-op heap sampler
before timed Do, 512 no-warm ops, then all warming ops until actual readiness
with at least 100 samples. Bound remains max(2*control p99,10ms); violations
fail the test. No production commit, original resource test change or tuning.
This is a synthetic shared device; its RED would prove the contention model,
not retrospectively prove the ended failing runner's cause. A passing result
will be reported as such. The simple fixed-sleep fixture was never run.

Coordinator35016b9/run37554203426 stopped before generating a fixture: the
test mixed signed op counts with an unsigned serial. Both jobs BUILD FAILED;
this is no latency evidence. The counters are corrected to unsigned counts.

Original acceptance coordinator4e635b9, run37553064830: COMPLETED FAILURE.
Five of six jobs pass; 1M repeat2 fails the external writer bound while ALL
six original memory/startup tests pass. The source remains exact a709e274.

| Records/repeat | Job | No-warm p99 ms | Warm p99 ms | Bound ms | Result |
|---|---|---:|---:|---:|---|
| 1M/1 | 112572852903 | 0.978552 | 1.188486 | 10 | PASS |
| 1M/2 | 112572853012 | 15.647965 | 37.429876 | 31.295930 | FAIL |
| 1M/3 | 112572852725 | 1.163811 | 1.325699 | 10 | PASS |
| 2M/1 | 112572852905 | 1.883051 | 2.180935 | 10 | PASS |
| 2M/2 | 112572852871 | 4.224905 | 6.104809 | 10 | PASS |
| 2M/3 | 112572853131 | 0.506928 | 0.638083 | 10 | PASS |

The failing runner was already slow in its no-warm arm; warming multiplies
that p99 by 2.39. Its 2,122 warming ops span 6.940s; incremental heap is
25.813376B/record and peak above steady is 24,681,392B, within original caps.
No stage or I/O evidence was collected on that exact runner, so reader-I/O
competition remains a hypothesis, not a measured cause. The runner has ended.
Prior passing diagnostics cannot replace measurements of this failure.

The original acceptance description below is historical. The paused/sampler
fixtures also remain historical and inactive. Pause run37552758193 and
redundant sampler37552519814 are terminal CANCELLED, not evidence. No trace
study, acceptance sampler change, production tuning or local execution.

The original acceptance coordinator checked exacta709 and a clean checkout, then added only
a separate no-warm control TEST. Every tracked source file, including the
original resource and warming tests, must remain unchanged. The original
generator, three heap/boot baselines and production probe run verbatim. A
separate2048-op process with the original per-op sampler supplies no-warm
p99; it reuses the paired ledger filename, preventing an extra large ledger
footprint, and no control ops or delays enter the production warming process.
The coordinator judges max(2*control p99,10ms) from checked receipts and
returns failure if that bound OR the original production probe fails. Any
failing fresh run is the reproduction to study next. Then the API.

Sampler A/B completed atc287db9 /37552231234 with both jobs SUCCESS and all
eight actual bounds PASS.1M original/bounded/bounded/original p99 were
1.219292/1.043874/1.230295/1.129534ms, controls1.163210/1.241579ms;
2M1.395218/1.282817/1.509349/1.679163ms, controls1.325296/1.368229ms.
All bounds were10ms.1M heap-snapshot counts4239/82/83/4227 and2M
7937/152/152/7975 show the intended frequency change occurred. There is
no material p99 collapse at either size, so the STW hypothesis is unconfirmed.
The following sampler description is historical, not the active experiment.

Both active source checkouts are now exacta709e274. Root`old` means the original
per-iteration ReadMemStats sampler; root`new` means100ms sampling, not a newer
production source. Only the existing warming TEST helper is overlaid with the
same bytes in both roots. Production code and the top-level resource probe
remain unchanged. The environment period is the difference between arms.
The same runner/fixture runs original/bounded/bounded/original. The ordinary
heap/boot baseline and its two repeats still run; a separate2048-op real
writer control with no first Accept supplies each arm's prespecified bound.
No control ops or four-second delay enter the production warming process.
The production probe starts the real builder at its usual first Accept and
retains its original2ms pacing, Measurement, rate refill and timed Do calls.
Receipts count heap samples, writer samples, peak and p99, and fail explicitly
on the unchanged memory/boot bounds or new writer bound.100ms peaks are sampled
peaks, not a proof that no shorter peak occurred. The causal experiment must
demonstrate a p99 collapse before a probe fix or cause claim; ReadMemStats
happens BEFORE timed Do in the original sampler, so direct overlapping stops
cannot simply be assumed. If p99 does not collapse, return to the accepted
reader-I/O pause fixture. One experiment at a time.

Request23228, architect56794. Production remains immutable at a709e274.
API work is parked until the latency cause is established. No tuning is in
this diagnostic. All execution is hosted; no local load or compiler runs.

The historical 3ef and a709 resource runs used different hosted runners.
Their writer p99 values alone do not establish a source regression or its
cause. The additional acceptance bound is warming writer p99 at 1M and 2M
records <= max(2 * same-runner no-warm p99, 10ms). The prespecified reference
here is the 2048-op control BEFORE serving, not a favourable choice among
controls. The after-ready control is reported separately as sensitivity.

Each job checks out exact 3efb598c5fe194a397157d3cd8b7fcdf76a2d452 and
a709e2746f4522591a112aefa0cace11e14e5687, asserts both are clean, and overlays
the same diagnostic test. One encrypted fixture/key is copied byte-identically
between them; ledger hashes are asserted equal. All arms replay the checked
fixture into the actual canonical State and default history adapter. The
actual Engine starts, reaches SubscribeInfo, and performs real AckBoard ops
through Do. The HTTP listener's first Accept triggers the actual private fold.
No production file, setter, manual warming flag, fsync substitute or core rule
is altered. Fresh test processes run in prespecified old/new/new/old order
on the same runner, each copying the same unmodified S0 fixture before Replay.

Every control has 2048 samples, each phase uses the same 2ms pacing, real
rate refill before timing, index Measurement call and measurement adapter.
Warming runs until actual readiness, with at least 100 measured ops and a
five-minute deadline. Append and ObserveMail durations are captured through
an embedded port adapter around the actual calls. Per-op counters prove
each measured AckBoard actually appended and observed exactly once. Reported
remainder is total Do time minus those two stages; it includes writer queue,
Apply, scheduling and other work and cannot alone distinguish them.

Runtime receipts include GC cycles/pause totals, heap live/goal/allocated
bytes, GC assist/dedicated/pause CPU and scheduler/GC pause histograms.
The slowest twelve calls carry their individual stages and GC counter/assist
deltas. A GC cycle delta is correlation, not proof of a pause overlap; CPU
metrics may be refreshed at GC boundaries. The port timers and metric reads
are identical between versions and controls, but perturb timing. No sampling
profile is enabled on these runs. These receipts are a causal diagnostic,
not a replacement resource/full acceptance gate or permission to tune.

The diagnostic logs latency-bound failures as failures in the receipt while
continuing all source arms; fixture, authority, replay, port-counter, readiness
or timeout failures fail the test/job. Read the actual receipts, not the job
badge, to assess the latency bound. If stage and runtime receipts cannot
separate causes, follow with explicitly labelled profiling on the same
sources; do not invent a cause from an unmeasured lock or historical timing.

Source comparison: the normal-record reader changes ReadBytes to ReadSlice
with copying only for oversized records. The per-record projector read lock
and 256-record yield exist in both sources. The remaining production changes
are bootstrap/build/drain extraction, comments and line formatting. None of
those observations establishes which change caused the reported p99.

## Initial receipts and next causal control

Exact coordinator31b78e9250df5b36873f6c4e9b4590e9fd9c09bd, hosted run37551161012,
completed successfully with all eight actual latency receipts below the
prespecified10ms bound. At1M old/new/new/old warming p99 were
0.661565/0.708908/1.036723/0.811360ms; before controls
0.556439/0.635866/0.742471/0.822702ms. At2M warming p99 were
2.465089/2.065275/1.986717/2.138071ms; controls
1.767630/2.063743/2.269323/2.160853ms. Warm Observe p99 <=0.010ms,
remainder <=0.159ms; GC pause histogram p99 upper <=0.164ms and
scheduler upper <=0.132ms. Largest2M tails were in old-1:
202.6255ms total /202.5149ms Append /0.003ms Observe /0.1075ms remainder.
New1M had rare16–18ms Append tails, but not a16ms p99. Historical16/64ms
p99 is not reproduced here, and its source cause remains unproven.
Append contains Seek, encryption, JSON, Write, Sync, hashes and recordMail
scalar assignment; it does not take the index mutex. These receipts do not
by themselves isolate fsync from the rest of Append. Coordinator Mac CI
failed six tool style checks; formatting/complexity/error wrapping are repaired
without changing thresholds. That lint failure is not a production finding.

Architect57036 asks whether reader I/O competes with writer flushes. The
follow-up retains the original resource probe's per-iteration ReadMemStats
call equally in every phase, closing that measurement gap. At2M, measure
2048 actual ops with the reader running, then2048 while it is paused at its
existing cancellation checkpoint. Only the context passed to the actual
ServingListener is pausable; the Engine receives the normal parent. The
pause handshake must succeed, BuiltSerial must remain fixed, and readiness
must remain false while the live writer continues. Resume and finish warming.
Combine ALL running and resumed warm samples for the bound; paused samples
are a causal control, never substituted into warming p99.

Linux /proc/self/io counters before and after every phase report logical
reads and actual disk read_bytes, plus writes. Count all Append and total
outliers >10ms in each phase. A zero disk-read delta with logical reads can
show page-cache service on this runner; source ReadAt alone cannot prove
physical disk I/O. Matched paused/running deltas distinguish a reader effect
from unrelated disk tails. All receipts remain labelled diagnostic; no tuning
or acceptance claim is made before measuring those controls.
