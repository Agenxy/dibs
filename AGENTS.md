# AGENTS.md: orientation for agents working on Dibs

Read `PHILOSOPHY.md` first; it is the decision procedure. This file is the map.
`docs/ARCHITECTURE.md` is the territory: how it fits together, and the four bug
classes that keep recurring here. If you are an agent *using* Dibs rather than
changing it, you want `SKILLS.md` (also served over MCP as `dibs://skills`).

## What this repo is

A Go daemon (`dibd`) + CLI (`dibs`) implementing a local coordination service for
fleets of AI agents. Agents connect over **MCP**. See `SPEC.md` (living, not frozen,
change it when reality disagrees, and record why).

## Layout

| Path | Role |
|---|---|
| `internal/core/` | **Pure deterministic state machine.** No I/O, no goroutines, no wall clock. `Apply(op, now) → (result, events)`. The invariant `state == fold(ledger)` lives or dies here. |
| `internal/ledger/` | Append-only, hash-chained, encrypted JSONL. Truth. |
| `internal/engine/` | Single-writer event loop over `core`. All impure inputs (liveness probes, clocks) are *recorded into ops* so replay reproduces decisions. |
| `internal/mcp/` | MCP surface: tools, resources, `subscriptions/listen`. Dual-version (2026-07-28 + legacy). |
| `internal/blobstore/` | Content-addressed attachment bytes, encrypted at rest. Outside the replay model by design. |
| `internal/web/` | Human board (SSE + htmx; one ~320-line script in `internal/assets/board.js`, no framework or build step). |
| `cmd/dibd/` | Daemon + auth gate. `cmd/dibs/` | CLI. |

## Rules you must not break

1. **`internal/core` stays pure.** No I/O, no `time.Now()`, no randomness, no network. If
   you need an impure input, record it in the `Op` so replay applies the same decision.
2. **An op is ledgered iff it changed replayable state.** The engine ledgers exactly when
   the serial advanced. Never let those disagree.
3. **Non-deterministic things live outside the core** as derived, rebuildable views.
   Losing a derived view must not lose coordination state.
4. **Advisory, not coercive.** Declaring work never fails. Don't add blocking semantics.
5. **Don't drive harnesses.** No shelling out to agents, no prompt injection, no session
   management. Dibs is pulled from, not a driver. (We built and deleted a shell-hook
   version: see `WAKE-MECHANISMS.md` for why.)
6. **Honesty in errors.** Every error carries a `hint` that tells a drifted agent the
   corrective call.

## Working here

**Once per clone**, before anything else. `task` itself is pinned by mise, so a
fresh checkout has no runner until this has run:

```bash
mise trust && mise install
```

Then:

```bash
task ci                   # THE gate: vet, lint, -race, build, 4 e2e suites + the
                          # sidecar contract, and the SPEC §17 coverage floor.
                          # Includes cross-compilation to all four release
                          # targets and govulncheck.
go build ./...            # quick build
go test -race ./internal/...
gofmt -w <files>          # always
```

**Check the exit status of `task ci`, not its output.** Grepping for "checks
passed" and concluding green is a mistake already made in this repository: one
suite printed a failure line that did not match the pattern, and a red run was
reported as green.

Lint alone: `mise exec golangci-lint@2.12.2 -- golangci-lint run`

Use **bun**, never npm, for any JS/TS work.

**No shell scripts.** Not for build steps, task running, hooks or test
harnesses: see `CONTRIBUTING.md` for why. Python with a `uv` shebang and PEP 723
inline dependencies, or Go, or the existing runner.

## Easy to miss

Things that have cost real time here, none of which are visible in the diff:

- **Validation belongs in `core.Admit`, never `core.Apply`.** `Apply` is the
  fold, and replay runs it over ops accepted by *older code*. A rule added there
  is retroactive: the daemon refuses its own ledger and will not boot.
- **`e.query()` sends on `e.ops`, which is nil on a zero-value `Engine`.** A test
  that builds `&Engine{}` and calls an exported wrapper **blocks forever** rather
  than failing. That is why the decision is split from the wrapper (`noteChild`,
  `reportStallLocked`): test the decision.
- **A parameter you declare but never read is invisible from outside.** The call
  succeeds and the effect silently does not happen; the schema is the only thing
  an agent can see. `TestEveryDeclaredParameterIsReadByAHandler` enforces this.
- **Renaming an op's json TAG is a silent data-loss bug, not a rename.** A
  retired op *kind* stops the fold, loudly. A retired *field* stops nothing: the
  op applies with that field zero and replay reports success. `lane_kind` →
  `agent_kind` shipped in every release to v0.0.4 and silently demoted every
  persistent agent to ephemeral on upgrade. Rename the Go identifier freely; the
  tag is frozen, and `TestLedgerFieldNamesAreFrozen` fingerprints the list
  because the same sweep that renames tags will happily rewrite the list that
  guards them, which is exactly how this got through.
- **A sweep rewrites the tests and comments that exist to catch the sweep.**
  After any find-and-replace across the tree, read the diff of every *guard*
  first: frozen-string tables, retired-vocabulary lists, fixtures, and the prose
  explaining why they must not change. One of those comments was left reading
  "renames the participant from `Agent` to `Agent`", and the guard beneath it had
  been turned into a list of the current vocabulary.
- **`light-dark()` takes colours only.** Using it for a number or a keyword is
  invalid at substitution and falls back to `initial`: silently. This shipped a
  completely unreadable board past 155 passing browser checks.
- **The space e2e scores against this repo's own git history**, which changes
  with every commit. It measures its bar at runtime. Never assert an absolute
  score; assert the property.
- **`SKILLS.md` has a copy at `internal/mcp/skills.md`** because `go:embed`
  cannot reach above its package. The root file is canonical;
  `skills_embed_test.go` fails if they drift. Edit the root one and copy.
- **A failing probe is usually a broken probe.** Before concluding the product is
  broken, check that your measurement is sound: assert your setup steps
  succeeded. Three false alarms in one session came from this.

## Where the reasoning lives

| Doc | What it settles |
|---|---|
| `PHILOSOPHY.md` | What Dibs is, is not, and the test for any change |
| `SPEC.md` | The protocol and its guarantees (living) |
| `REQUIREMENTS.md` | The measured real-world failure that defines the requirements |
| `WAKE-MECHANISMS.md` | How agents learn about events; what was rejected and why |
| `SPEC-ATTACHMENTS.md` | Blob/attachment design |
| `docs/ARCHITECTURE.md` | Structure, request path, invariants, recurring bug classes |
| `SKILLS.md` | Agent-facing: how to USE Dibs well (served as `dibs://skills`) |

## Distribution

Releases are cut by tagging: the workflow re-runs the whole gate against the tagged
commit, then publishes signed artifacts, updates the Homebrew cask in `agenxy/homebrew-tap`,
and publishes `server.json` to the official MCP Registry as `io.github.Agenxy/dibs`.
Nothing here needs a human between the tag and the release, and no source is updated by
hand: if the three ever disagree, that is a bug in the pipeline, not a chore.

**We do not pay to be listed** (PHILOSOPHY.md rule 8). Several MCP directories now gate
submission behind a fee, and the answer is no every time, however good the traffic
numbers look. Free listings, PRs to community lists, and registries that index from the
official one are the whole strategy. When a directory says "publish to the Official MCP
Registry and we will pick you up", that is the strategy working.

Before submitting to a community list, READ ITS RULES. Several set a minimum age or star
count and auto-close everything else; submitting anyway spends a maintainer's attention
to no purpose and is the kind of thing that gets a project remembered for the wrong
reason.

## Testing expectations

Behavioural tests, not coverage theatre. Every guarantee in `SPEC.md` that can be tested
should have a test that fails if it regresses. Replay determinism, advisory-not-coercive
semantics, and the honesty rules are the ones that matter most: see
`TestRedundantObjectiveIsCaught` and `TestConcurrentFileWorkIsNotAnAlarm` for the
shape: each encodes a *decision*, with the reasoning in the comment.
