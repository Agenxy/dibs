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
