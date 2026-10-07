# Paired writer-latency cause measurement (disposable, never merge)

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
