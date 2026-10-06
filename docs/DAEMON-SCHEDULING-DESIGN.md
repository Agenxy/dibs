# Daemon scheduling under host load

Request 45633, 2026-10-05. Proposal; no installed policy has changed.

## Accepted measurement

Architect 46109 accepted Standard and stopped further experiments. The hosted
receipt is run [37392623433](https://github.com/Agenxy/dibs/actions/runs/37392623433)
at proof head 052d23c, whose production source is 7d32560. This is a partial ABBA
comparison, not a completed three-round result. On rootless uid 501, macOS
26.6.2 (25G83), arm64, three logical CPUs and six CPU workers:

| Loaded arm | Send attempts | Unknown outcomes | Send p95 | Send maximum | Respond p95 |
|---|---:|---:|---:|---:|---:|
| Background | 15 | 10 | 14,797.87 ms | 14,797.87 ms | 7,641.15 ms |
| Standard 1 | 50 | 0 | 11.68 ms | 20.61 ms | 13.10 ms |
| Standard 2 | 50 | 0 | 14.79 ms | 23.77 ms | 9.88 ms |

The Background arm hit its 120-second wall budget (129.12 seconds including
the final calls); the Standard arms took 6.86 and 7.38 seconds. All requests
used the same real daemon binary and authenticated stateless MCP. Unknown
initial sends were recorded without retrying or fabricating a response serial.
The next Background launch never reached running within the fixture's
20-second startup bound, preventing the remaining arms. These observations
support removing the Background restriction; they neither isolate all causes
of the operator's earlier four-minute gap nor establish a universal SLA.

Competitor work was 954,776 units/s in Background versus 782,604 and 889,381
units/s in Standard (about 7–18% lower). Different arm lengths, timer pacing and
a single hosted machine limit that cost comparison. This is not an energy
measurement. Daemon CPU time was 0.09 seconds in Background and 0.15/0.12
seconds in Standard; those totals also cover different sample counts.

Rootless capability probes showed launchd honoring Background, Standard,
Adaptive and Interactive and a requested Nice=-5. The native fixture's default
thread QoS values were respectively 9, 17, 21 and 21; a separate pthread
user-initiated request returned success and self QoS 25 in all three tested
Background/Standard/Interactive processes. This measures that native thread's
requested class, not the effective scheduling of every Go thread or the
daemon's migrating writer goroutine. Standard alone suffices for this fix;
neither negative nice nor a runtime thread-QoS adapter is shipped.

The product Go source has not changed since this measurement. Later fixture
work that prewarms services was canceled at the architect's direction; its
unrun extensions are not evidence for these figures. All nine new regression
leaf cases were intended RED on a3fa018, with current guards and the unchanged
operator-policy/other-board controls GREEN in this hosted receipt. Full
three-platform CI must pass on the final source commit before merging.

## Observation and boundary

The operator reported load average 29, a 4m22s daemon log gap, a two-second
`open` timeout and a roughly one-minute response with an unknown send outcome.
Those observations do not distinguish CPU starvation from blocking I/O or a
stalled request. We can establish the scheduling policy and measure whether it
contributes, without loading the operator's machine.

On macOS 27.0 (26A428), the installed `org.agenxy.dibs` user LaunchAgent has
`ProcessType=Background`, no `Nice` or QoS keys, and no explicit I/O priority.
`launchctl print gui/501/org.agenxy.dibs` reports `spawn type = background (5)`;
the daemon and its observed OS threads have nice 0 and priority 4. Priority is
not a measurement of requested thread QoS, and Go goroutines move between
threads. The installed daemon is f299c6e; the current source template also
specifies Background. Read-only receipts are in `/tmp/dibs-scheduling-45633/`.

`task install` replaces signed artifacts, then advises `dibs upgrade`; it does
not write or reload a LaunchAgent. `dibs configure --service` creates the unit.
`dibs upgrade` currently repairs binary/directory drift only, so changing the
template alone would leave an existing correctly pinned Background unit intact.
Its restart already bootouts and bootstraps the unit before kickstart, which is
necessary for a changed plist to take effect.

## Proposed policy

Architect accepted in 45687 with Standard preferred. Use `ProcessType=Standard`,
leave nice at its default 0, and add no runtime
thread QoS calls. Dibs serves requests whose delay stalls the person's running
agents. Apple's installed `launchd.plist(5)` says Interactive has application
resource limits, while Background intentionally limits CPU and I/O. Adaptive
changes class on XPC activity; Dibs' HTTP/socket traffic cannot supply that
relationship. Apple explicitly identifies that limitation for non-XPC IPC in
[its Apple silicon guidance](https://developer.apple.com/documentation/apple-silicon/tuning-your-code-s-performance-for-apple-silicon).

Interactive is approved only if the hosted Standard arm proves insufficient.
This removes a discretionary background restriction; it promises neither
capacity nor bounded latency. All daemon work, including indexing, shares that
process policy. Greater competition with other applications and greater energy
use are costs to measure. No timer-coalescing or I/O-specific override is added.

Negative nice is not the proposed fix. Apple's
[setpriority contract](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/setpriority.2.html)
requires privilege for raising priority; whether current user launchd can apply
a plist's negative nice before dropping credentials needs a rootless launch
measurement, not inference from that syscall contract. Explicit user-initiated
QoS is a thread/work-item API, not a documented general LaunchAgent plist key.
Setting it on one Go thread would not establish it on the writer's goroutine or
future runtime threads. A native runtime adapter would exceed this fix's scope.

## Existing installations

Detect the top-level Background value on the current, board-matching macOS unit
during upgrade planning. Include that drift in dry-run, writable preflight,
`nothingToDo`, and reconcile. For policy-only drift, replace only the value's
XML text, preserving the rest of the unit and retaining a `.replaced` receipt.
The parser must distinguish top-level keys from nested EnvironmentVariables,
reject duplicate/invalid structure before stopping anything, and give a remedy
for unsupported binary plists rather than silently rewriting them. Leave an
operator's explicit Standard/Adaptive/Interactive value alone. A different
board's unit remains outside this upgrade. Existing binary/path migration keeps
its current regeneration/backup contract and gets the new template policy.

## Hosted proof before claiming improvement

One isolated hosted macOS job; no local load, app opening, installed-file edits,
live-board requests or root. Record OS, architecture, logical CPU count and uid.
First bootstrap disposable user LaunchAgents with Background, Standard,
Adaptive, Interactive and Interactive plus Nice=-5; assert the launch succeeded,
read launchctl's loaded role, actual PID and nice/OS-thread priority, and report
refused or ignored settings distinctly. Use a native thread-QoS reporter only
as a fixture measurement, never as a claim about Go's writer thread.

Then launch the same real dibd binary on isolated boards under Background and
Interactive. Establish unloaded baselines and three paired ABBA rounds under a
fixed CPU-bound competitor load (twice the logical CPU count). Register agents
through stateless MCP 2026, then measure acknowledged send and response calls
through the actual authenticated, ledgered path. Record every call's monotonic
RTT, errors, p50/p95/p99/max, competitor work throughput and process CPU time.
Assert all setup, all expected samples and live load workers, and bound cleanup
and the whole run. The CPU competitors keep the same normal policy in both
arms. Preserve raw samples, logs, plists and launchctl receipts as artifacts.

If the runner does not expose a usable user launch domain, or the measured load
does not separate the arms, report the proof gap rather than calling it an
improvement. Rootless policy application and request latency are separate
findings. The hosted fixture is a bounded experiment, not a regular timing
threshold in `task ci`.

Regression tests enter through unit generation and upgrade's real planning/
preflight/reconcile paths. Prove intended failures on the preceding production
code, including policy-only upgrade, same-build no-op, customization retention,
nested/duplicate keys, different-board isolation, read/write refusal before stop,
and loaded policy after the existing reload path. Obtain architect review and
the exact-head three-platform gate before merging. Installation/release remain
held by the person.
