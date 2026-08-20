# Architecture

How Dibs is put together, why it is put together that way, and what you need to
know before changing it. If you are fixing a bug, the useful question is usually
not "what does this code do" but "what was it supposed to do": this document is
the second one.

Companion documents: [SPEC.md](../SPEC.md) is the design (v1.1, living),
[SPEC-CHANNELS.md](../SPEC-CHANNELS.md) covers duplicate-work matching,
[SPEC-SUPERVISION.md](../SPEC-SUPERVISION.md) covers stall detection, and
[PHILOSOPHY.md](../PHILOSOPHY.md) is the boundary the whole thing is drawn
around.

---

## The one-paragraph version

Every coordination fact is an **op** appended to an fsync'd, hash-chained JSONL
**ledger**. State is exactly the fold of that ledger. `state == fold(ledger)`,
so a restart replays rather than recovers, and the persistence *is* the audit
history. All mutation goes through a **single-writer event loop** over a **pure
state machine**, and one monotonic **serial** totally orders everything that has
ever happened. That is the whole design; the rest is surfaces onto it.

## Why single-writer, when Go has mutexes

Because the property that matters is not mutual exclusion, it is **determinism**.
If mutation is a single serialized stream of ops over a pure function, then
replaying the ledger reproduces the state *exactly*, which makes the entire
system simulatable: the tests drive randomized op and time sequences and assert
that replaying gives an identical board. A mutex-guarded shared struct gives you
safety without giving you that, and the moment two goroutines can interleave
writes, "what happened here" stops being answerable from the log.

The cost is that the writer must never block. Anything slow, process scans,
embedding calls, HTTP, happens *outside* the loop, and only the verdict goes
back through it.

## Layout

```
cmd/dibd        the daemon: MCP server, web board, supervision sweep
cmd/dibs         the CLI: board, doctor, probe, await, calibrate, admin

internal/core     the PURE state machine. No I/O, no clock, no goroutines.
internal/ledger   append-only hash-chained JSONL, fsync, replay
internal/engine   the single-writer loop; owns state, drives core, pushes notices
internal/mcp      the MCP surface: tools, resources, MCP Apps panel
internal/web      the operator's web board (server-rendered + SSE)
internal/ui       the terminal board
internal/assets   the design system shared by web and panel
internal/overlap  duplicate-work scoring: tier 0 (files+history), tier 2 (embeddings)
internal/liveness process forensics: is this spawned agent working, or stuck?
internal/paths    where data lives
internal/build    the one version string every surface reports
```

The dependency direction is strict: `core` knows about nothing, `engine` knows
about `core` and `ledger`, and the surfaces know about `engine`. Nothing flows
back up. If you find yourself wanting `core` to import something, the design is
telling you the logic belongs in `engine`.

## The path of a request

1. A surface (`internal/mcp`) decodes arguments and **authenticates**: the
   coordination secret for the machine, plus an agent token for anything
   agent-scoped.
2. It builds a `core.Op`.
3. `core.Admit` validates it. **This is the only place validation belongs.**
4. `engine` sends it through the writer loop.
5. `core.Apply` folds it into state. This is a *pure function* and must stay one.
6. `ledger` appends it and fsyncs.
7. The reply, plus any notices, go back out.

### The rule that catches people: validation goes in `Admit`, never `Apply`

`Apply` is the fold. Replay calls it on every op that was ever accepted,
including ops accepted by *older versions of the code*. So a rule added to
`Apply` is retroactive: the daemon starts refusing its own historical ledger and
will not boot. New rules go in `Admit`, which runs at ingress and never during
replay.

This has been got wrong more than once. If your change makes `Apply` return an
error under a new condition, it is almost certainly in the wrong place.

## What must stay true

These are invariants, not preferences. Each was learned expensively.

- **`state == fold(ledger)`.** Anything that makes replay produce a different
  board than the live one is a bug, even if both look fine.
- **The writer loop never blocks.** No process scans, no network, no file reads
  inside it.
- **A serial is never reused and never goes backwards.**
- **Nothing acts on an agent's behalf.** Dibs reports. It will tell a parent its
  subagent is stuck and hand back the resume command; it will not run it. See
  [PHILOSOPHY.md](../PHILOSOPHY.md): this one is the product, not an
  implementation detail.
- **Ephemeral observations stay out of the ledger.** Which processes happen to be
  running is a fact about this machine right now; it does not survive a restart
  and should not. Child sessions and stall verdicts are in-memory.
- **A wrong answer is worse than none.** Attribution walks a ladder and records
  which rung it came from. If no rung matches, the answer is "unattributed": not
  a guess. A stall reported to the wrong parent is worse than one reported to
  nobody, because the agent that could act never hears it.

## Where the recurring bugs come from

Four classes, all of which have shipped here at least once. They are worth
knowing because they are the ones review does not catch.

**A. Built but unreachable.** Code that is present, correct, and wired to
nothing: a validator nobody invoked, a tool implemented but never declared, a
parameter agents were told about that no handler read, a threshold rung never
called from the classifier. Everything *looks* present, which is why reading the
diff does not find it. There are now machine checks for some shapes,
`schema_reach_test.go` parses the package to prove every advertised tool
parameter is actually read, and adding one for a shape not yet covered is a
genuinely valuable contribution.

**B. Tests that prove the wrong thing.** A thinking-state test that still passed
with the CPU signal deleted. A coverage gate that counted operations it never
exercised. A contrast test measuring the wrong pairs. The defence is mechanical:
**watch your test fail for the reason you think it fails**, before you believe
it.

**C. Threshold zeroing.** A config built from flags where an unset flag means
zero, and zero means "everything is stuck" or "nothing is" depending on the
comparison: silently disabling a check. Every config now layers over
`DefaultConfig()` and overlays field by field.

**D. Green suite over a broken surface.** 155 browser checks passed against a
board that was completely unreadable, because `light-dark()` had been used for a
number and a keyword, where it is only valid for colours, so it fell back to
`initial`. Visual assertions must assert something *painted*, not merely present.

## Coupling to look out for

The space end-to-end suite scores declarations against **this repository's own
git history**, which grows every time anyone commits. It therefore measures its
join bar at runtime rather than hardcoding one: a fixed threshold passes until
somebody makes a commit, then fails for a reason no contributor can act on. If
you add an assertion about an absolute score, it will rot. Assert the
*property*: above the bar joins, below it advises.

## Adding a tool

1. Declare it in `internal/mcp/tools.go`: name, description, JSON Schema. The
   description is the **only** thing an agent ever sees, so write it for a reader
   with no access to the code.
2. Add its arguments to `toolArgs`.
3. Handle it, and **read every parameter you declared**. A documented parameter
   no handler consumes is indistinguishable from a working one, from outside:
   the call succeeds and the effect silently does not happen.
   `TestEveryDeclaredParameterIsReadByAHandler` enforces this.
4. If it mutates, add the op to `core`: validation in `Admit`, fold in `Apply`.
5. Add it to the end-to-end suite, which drives a real daemon over real HTTP.
   The Go tests prove the state machine; the e2e proves the *wire*, which is
   where a field name not matching a JSON tag turns a recorded score into a zero
   and nobody notices until replay.

## Surfaces are not the same page

The MCP Apps panel and the web board share a design system
(`internal/assets`) but are deliberately different products. The panel is **one
agent's** own board and mailbox, authenticated by that agent's token, rendered in
a host's sandboxed iframe under a CSP with no external origins, so every asset
is inlined, and a stylesheet fetched over the network would fail closed and
silently. The web board is the **operator's** view over every agent and all mail,
behind proof that a human is here: Touch ID where the machine can check it, and
the admin password where it cannot.

They answer different questions for different readers. What they share is what a
agent, a message and an event *look* like.

## Protocol versions

Dibs targets **MCP 2026-07-28** (stateless core) and serves the legacy
**2025-11-25** path. Surveyed from harness sources on 2026-08-03, no shipping
host negotiates 2026-07-28: Codex tops out at 2025-11-25 with the newer version
behind an under-development flag, opencode at 2025-11-25, Gemini CLI at
2025-06-18, Hermes at 2025-03-26. The legacy path is therefore **load-bearing,
not vestigial**, and must not be removed on the assumption clients have moved
on. Deprecated features are guaranteed for at least twelve months from the
2026-07-28 publication.

Implemented from the stateless core: no-handshake operation, `server/discover`,
per-request version and identity, subscriptions on both paths, and cacheable
list results.

**The two version lists are deliberately disjoint.** `handshakeVersions` is what
`initialize` may negotiate; `modernVersions` holds `2026-07-28`, which retired
that handshake. They were one flat list, so `initialize` with `2026-07-28` was
echoed straight back: the server agreeing to a stateless contract over the path
that contract removed. The reference SDKs make the same split
(`HANDSHAKE_PROTOCOL_VERSIONS` / `MODERN_PROTOCOL_VERSIONS`), and
`TestHandshakeAndStatelessVersionsDoNotOverlap` keeps them apart.

**Cache hints are a permission, not just a hint.** `internal/mcp/caching.go`
stamps `ttlMs` and `cacheScope` on every result the spec requires. `ttlMs` being
wrong costs a stale read; `cacheScope` being wrong is a disclosure bug,
`"public"` tells shared gateways and proxies they may serve a response to a
caller in a *different authorization context*. So `dibs://inbox` is `private`
and everything identical-for-all-callers is `public`, and
`TestAPrivateMailboxIsNeverMarkedShareable` fails if that inverts.

A note on surveying this yourself: grepping for `2026-07-28` finds dates. The
first pass here reported Hermes as supporting it, on the strength of a match
that turned out to be a billing-cycle fixture in a subscription test
(`cycle_ends_at="2026-07-28"`). Read the context, and prefer the constant the
code actually negotiates with.
