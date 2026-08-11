# What Dibs is

**Dibs is a coordination service for the fleet of AI agents running on one machine**, across whatever projects they are working on.
It gives agents *situational awareness*, who is here, what they are pursuing, what has
already been tried, so they stop duplicating each other's work.

It is a **service agents pull from**, never a harness that drives them. Nothing in Dibs
can make an agent do anything; the strongest thing you can receive is a message you may
decline.

## The failure it exists to prevent

Two agents independently pursue the same objective. Neither knows. Both burn tokens and
time; the redundant work races to the lowest quality bar; a human eventually plays
message bus between them. Measured instance: `REQUIREMENTS.md`: three PRs, ~3,900 diff lines,
one goal, ~1,200 lines directly wasted.

The general class: **coordination failures among parallel agents sharing a project.**
Redundant objectives · lost handoffs · unknown status · repeated dead ends · contention
over the few genuinely exclusive resources.

## What Dibs is not

- **Not an orchestrator.** It never assigns, schedules, or spawns work. Agents and humans
  decide; Dibs informs.
- **Not a mutex over source files.** Concurrent edits are normal and healthy: version
  control solved that, and suppressing it would destroy the parallelism that makes a
  fleet worth running. Real exclusion is reserved for things git does *not* isolate: a
  discrete work item (a PR/issue), a local install, a device, a port.
- **Not a harness, wrapper, or process manager.** It does not shell out to drive agents,
  inject into their prompts, or manage their sessions.
- **Not an industrial framework.** It should feel like a Unix utility: one job, done
  well, composable with everything else.

## The three pillars

### 1. Efficiency
Lightweight, fast, and scalable to large fleets. Unix-utility character: simple to run,
cheap to keep running, boring to operate. Resource budgets are **profiles, not dogma**,
a laptop-scale default that stays small, and honest headroom to scale up when a fleet
demands it. We do not buy features with permanent bloat, and we do not cap ourselves out
of correctness.

### 2. Usability
Easy to maintain, extend, and contribute to. **API-first**: every capability is reachable
programmatically, so Dibs can be driven by agents (MCP), humans (CLI, web), scripts, and
**editor/IDE plugins**: a tool window beside the agent window is a first-class target,
not an afterthought. Interfaces are contracts: stable, documented, versioned.

**Modularity is a usability feature.** Storage, semantic index, and embedding are
**ports with swappable adapters**. Someone who needs Postgres, or an existing vector
store, or a different embedder, should be able to plug it in without forking. Defaults
must be excellent and dependency-free; alternatives must be possible.

### 3. Community
FOSS and genuinely approachable. Architecture, organization, and code quality are
features: they are what makes contribution possible. Non-negotiables: real tests,
one-command bootstrap/build, `AGENTS.md` so an agent can orient itself in minutes, and a
philosophy clear enough that nobody has to guess what belongs here.

**Openness is the strategy.** We speak open protocols (MCP today) rather than inventing
private ones, so Dibs can be adopted, embedded, and extended by tools and communities we
will never meet. If someone wants Dibs to do more, the answer should be "build on the
API", not "fork it."

## Rules that follow

1. **Honesty over comfort.** Every guarantee stated is one the system can enforce; every
   limit of enforcement is stated with it. Claims expire: expiry means *coordination was
   lost*, never *it is safe to proceed*.
2. **Advisory by default, exclusive by exception.** Declaring work never fails. Only
   discrete, genuinely-ownable resources support real exclusion.
3. **Determinism at the core.** The ledger is truth: append-only, hash-chained,
   replayable (`state == fold(ledger)`). Anything non-deterministic, embeddings,
   rankings, liveness probes, lives *outside* the core as a derived, rebuildable view.
   Losing a derived view must never lose coordination state.
4. **Ports and adapters.** The core depends on interfaces, not implementations. New
   backends are additive.
5. **Language follows the problem, not the flag.** Go owns the core: static binaries with no cgo and no runtime deps,
   fast start, low RSS, correctness-critical replay. Other languages are welcome at
   component boundaries where the ecosystem is genuinely better: with one hard rule:
   **the component ships inside Dibs' artifact and is lifecycle-owned by Dibs.** A
   vendored native library is fine. "Install and run this other service" is not.
6. **The agent is the intelligence.** Dibs narrows and presents; it does not pretend to
   semantic verdicts it cannot justify. Return ranked candidates and let the model judge.
7. **No sprawl.** Every component must earn its place against the pillars. When in doubt,
   expose an API and let someone else build it.

## The test for any proposed change

> Does it help a fleet of agents avoid stepping on each other, without becoming a
> harness, without lying about what it enforces, and without making Dibs heavier to run
> or harder to contribute to?

If not, it belongs on top of the API, not inside Dibs.
