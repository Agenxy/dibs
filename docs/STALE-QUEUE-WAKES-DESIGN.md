# Stale queued wake notices

Request 36084, after the strict-hook fix in PR 354.

## Measured surface

The installed executable is
`/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex`, reporting
`codex-cli 0.160.0`. Its `queue --help` exposes only enqueue:
`codex queue [OPTIONS] --thread <THREAD> --message <TEXT>`.
Both `queue list` and `queue delete` exit 2 with an unexpected-argument error.
Neither call names a thread or changes a queue. This is a measurement of the
supported CLI, not a claim about every upstream app-server capability.

Dibs already has an experimental read-only `thread/queue/list` observer.
`boardconfig.ReadOnlyQueueProbe` and its behavioural tests intentionally permit
only initialize, initialized and list. This proposal does not add a mutating
app-server path or touch the harness database.

The original incident's queued-item ancestry remains an inference: its old
daemon logged a queued/open-deferred wake, but that line does not identify the
specific item later consumed. Another observed activation of this worker at
2026-10-05T08:41:09Z carried a new-request notice while both check_in and inbox
at serial 36275 contained no unread mail. That observation alone does not
identify its queued item or prove a causal race.

## Accepted fallback

Use a factual notice with a locally generated UTC timestamp on the native
queue route, for example:

> Dibs: question notice issued at 2026-10-05T09:50:00Z. It may already be handled.

The time means the queue attempt's admission time, before executing the CLI;
it is not a receipt proving the app accepted or displayed it. It includes a
date and UTC offset, so it remains useful across midnight and machine time
zones. The notice makes no present claim that unread mail exists. It has no
imperative, participant names or message body. It does not prevent the model
turn bought by an already queued item; it makes that item's meaning honest.
Architect answer 37116 refines the accepted text to remove the article and
duplicate word, maps both the blank kind and `notice` to `coordination`, and
uses second precision. The observer requires a strict UTC RFC3339 round trip;
fractional seconds and alternative offsets are not the emitted wire format.

Format at the shared native queue admission door in `internal/wakeexec`, after
the pending-item decision, so both daemon and host-bridge commands use it.
Keep ordinary exec and socket text unchanged. Never refresh an existing
pending item's timestamp when later mail coalesces into it. Recognize both
historical notices and the complete new format, including strict timestamp
validation, in the queue observer. Leave admission locking, delivery marks,
replay and wake policy unchanged.

## Accepted one-stage rollout

An old live host-bridge's `isDibsWake` recognizes only exact historical text.
After a new writer queues the timestamped notice, that old observer reports
known-but-no-Dibs-item and can bypass the shared pending receipt, enqueueing a
duplicate. New code accepting old wording solves only one direction. The
in-session stdio socket bridge is a separate route and cannot be assumed to
be the writer in this case.

Architect answer 36967 accepts the text and a one-stage switch, without an
enablement mechanism. The dormant-bridge rule protects against silent loss or
misreading. Here the transition failure is toward waking: an old live bridge
may queue one extra old-format notice per thread alongside the new item. That
extra item says exactly what the old writer already said; it cannot suppress
delivery. New writers recognize both formats and coalesce an old pending
item. Unknown, malformed or unowned text never counts as a pending Dibs wake.

The mixed-image interval ends as old writers are replaced or re-exec. It is
not a claim that every live writer has already upgraded. A staged reader-first
switch would need a new liveness/version proof to establish that old writers
were gone, in order to prevent a bounded duplicate. The accepted trade follows
the operator's rule that waking agents is non-negotiable: tolerate that
duplicate rather than introduce a mechanism that can strand a wake. The first
install's caveat belongs in the CHANGELOG as well as this note.

## Required guards

Enter through `RunCommands` and the existing compiled codexqueue subprocess
fixture, with setup failures fatal. Verify an actual queued item's factual
text and parseable admission timestamp, no participant/body disclosure, an
unchanged timestamp and one pending item after subsequent mail, and continued
coalescing across independent processes. Cover old and new pending formats,
new event vocabulary, unavailable observation, and ordinary non-queue text.
Drive the old-text-pending/new-writer transition through the same command door.
The new emission and dated-item recognition assertions must fail on old
production, then pass on the fixed source. Legacy coalescing, unchanged
ordinary commands and conservative fallback are preservation controls, not
claims of newly failing old behaviour. Use hosted full gates while the shared
local compiler seat belongs to K7.
