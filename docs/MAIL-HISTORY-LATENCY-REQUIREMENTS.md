# Mail-history latency acceptance revision

Request 23228; requirement owner dibs-architect, message 57658, 2026-10-06.
This is an explicit product-requirement revision, not a fix to a failing probe.

The earlier requirement (56794) was warming writer p99 <=
max(2 * same-runner no-warm p99, 10ms). It remains the historical criterion:
source a709 failed one of six original jobs, and candidate e771 also failed
one of six, at 15.158769ms warming against 2.800758ms control and a 10ms bound.
Those results remain FAIL. Synthetic disk-model GREEN did not clear them.

The architect states that the 10ms startup floor was chosen to detect an
earlier 64ms tail and had no independent product basis. The revised objective
permits a small temporary cost while a derived audit index is being built once
after boot, while protecting ongoing coordination after it becomes ready.
The architect explicitly owns this decision and said they would notify the
person. The revised warming limit is LOOSER than 56794:

- Warming p99 <= max(3 * same-runner no-warm p99, 25ms).
- For the 30 seconds after readiness, p99 <= max(2 * control p99, 10ms).
- Warming must finish within the existing five-minute probe deadline, including
  the previously verified back-to-back writer workload.
- All six fresh jobs (three each at 1M and 2M) must pass. No retroactive
  reclassification of the completed original acceptance jobs.

The original encrypted generator and memory/startup resource probes still run
verbatim in each job. A separate production-door fixture replays a byte-identical
ledger, starts the real Engine, measures 2048 no-warm operations, starts history
through first HTTP Accept, measures every warming operation until readiness,
and measures real coordination operations for 30 seconds afterwards. It uses
the original per-operation heap sampler, rate refill, timed Do and 2ms pacing.
The timed operation is the accepted AckBoard writer-path proxy, not a measured
end-to-end MCP check_in round trip. HTTP supplies the real serving boundary.
No artificial disk cost, activity setter, paused reader or production-source
change is introduced. Tracked source must remain byte-identical to the pin.

Each phase records wall time, sample count, GC mark-assist CPU seconds and the
scheduler-latency histogram, including the histogram's actual bucket boundaries
and count deltas. These are process-wide measurements: increased assist or
scheduler latency is correlation and cannot alone assign a cause to a writer
operation. Any failure is preserved with these receipts for investigation;
there is no claim that a synthetic model establishes a physical runner's cause.

The 1M/2M measurements qualify those fixtures, not every possible ledger or
storage device. The runtime builder observes its daemon context; the five-minute
deadline here is an acceptance-test bound, not a newly claimed runtime timeout.
