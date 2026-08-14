# Why Dibs exists: the requirements, from a measured failure

Derived from a real incident in a private multi-agent fleet. Details are withheld;
the numbers are real. This is the requirements spec: a design that doesn't prevent
*this* is not worth building.

## The failure

Two agent sessions, each driving subagents, independently decided to fix the same
broken CI gate. Neither knew about the other. Three overlapping pull requests
resulted, together ~3,900 changed lines across ~240 file-changes chasing **one goal**.
Roughly **1,000–1,200 lines were pure duplicated effort.**

Three properties made it worse than simple waste:

1. **It raced to the lowest quality bar.** One redundant PR took an accept-the-debt
   shortcut (raising budget/baseline files) for the exact findings another PR had
   *fixed at source*. Merging the redundant one would have silently undone the better
   fix.
2. **Detection was post-hoc and manual.** The overlap surfaced only when one agent
   happened to go looking. No system reported it.
3. **A human was the message bus.** The operator had to ask one agent to write a scope
   claim, then hand-relay it to the other. Ownership afterwards was still guesswork.

## Git worktrees did not help

Both sessions ran in **separate worktrees**. Isolation was perfect and irrelevant:
worktrees prevent *merge conflicts*, not *duplicated effort*. Two agents can work in
pristine isolation and still solve the same problem three times.

Any "just isolate them" answer, including editor features that put each agent in its
own worktree, does not address this. The missing layer is shared awareness.

## What the agents invented under pressure

Left to themselves, they converged on: declare what you own, partition by subsystem,
and send a request to stand down. That is *slots*, *claims*, and *typed messages with
responses*: reinvented by hand, badly, after the cost was sunk.

## Requirements

| # | Requirement |
|---|---|
| R1 | **Pre-flight visibility**, ask "is anyone already doing X?" and get an answer *before* starting. |
| R2 | **Declared intent**, not just file locks, scope is known before the file list is. |
| R3 | **Objective-level duplicate detection**: the waste was redundant *effort*; overlapping files were incidental. Two agents pursuing one goal must find each other even in different files. |
| R3b | **Claims are communication, not a mutex.** Concurrent edits to one file are normal and healthy; suppressing them would destroy fleet parallelism. Real exclusion is the rare case, resources version control does *not* isolate (a local install, a dev server, a device, a discrete work item). |
| R4 | **Typed messaging with responses**: a scope claim is a *request* needing approve/deny, not a broadcast. |
| R5 | **Delivery without a human in the loop.** |
| R6 | **Cross-harness**, one fleet spans several agent products. |
| R7 | **Work-item awareness**, collisions are often visible at PR/issue level before file level. |
| R8 | **Durable and auditable**, sessions restart, compact, and die; declarations must outlive a context window. |
| R9 | **Advisory, never coercive**, one of the three overlapping efforts was genuinely complementary. A hard lock would have blocked real work. |

## Non-goals

- **Orchestration.** Agents and humans decide; Dibs informs.
- **Merge-conflict prevention.** Version control already does that, and it wasn't the failure.
- **A hard mutex over source files.** See R3b/R9.
- **Solving only this instance.** The class is *coordination failures among parallel
  dibs sharing a project*: redundant objectives, lost handoffs, unknown status,
  repeated dead ends, and contention over the few genuinely exclusive resources.

## R10: an agent's liveness model must fit its surface

Discovered by running a chat-surface agent against a lease tuned for
continuously-running processes.

A chat agent only touches the API when its human types, so multi-minute silence
is its normal state, not a failure. Under a 5-minute lease such an agent flaps
`stale → recovered` forever while nothing is wrong, and it was reported as
`proc_alive: false`: a claim about a process that had never been declared,
because `alive[0]` returns the zero value. A human reads that as "it crashed".

Requirements:

- **Never assert a process state that was never claimed.** No PID given ⇒ no
  `proc_alive` in the event, and the stale reason is `idle_no_activity`, not
  `lease_lapsed`.
- **Grace scales with what can actually be checked.** An agent with a PID can be
  probed directly, so a short lease is safe: death is detected by the prober,
  not the clock. An agent without one can only be judged by silence, and gets
  `IdleTTL` (45m) instead of `AgentTTL` (5m).
- **Staleness is a statement about coordination, never about health.** Dibs
  knows an agent has not spoken. It does not know why, and must not imply it.

## R11: reaching the daemon is not the same as being a participant

Found by an agent whose token had been invalidated: `inbox` and `await_events`
rejected it with `E_BAD_TOKEN`, and in the same second `board` accepted the
same token and rendered the whole board to a human.

Two distinct holes, one behind the other:

1. **`board` deliberately did not authenticate.** The code said so, in a
   comment reading *"the board is public and still worth showing"*. It is not
   public: every other tool requires an agent token, and the board carries agent
   descriptions, working directories, hostnames and branch names.
2. **After that was closed, an empty token still passed**, because the check went
   through `SubscribeInfo`, which short-circuits on `token == ""` to serve the
   token-less board subscription that `subscriptions/listen` uses. It has the
   shape of an authenticator and is not one.

Requirements:

- **Every tool that returns board or mailbox state authenticates first**, with no
  exceptions justified by the data being "public". If it is worth rendering to a
  human, it is worth a token.
- **A function is an authenticator only if it rejects the empty credential.**
  `SubscribeInfo` does not, by design; anything using it for authorisation must
  reject empty input itself, and say why.
- **The local secret is a reachability proof, not an identity.** It says the
  caller is on this machine. Participation requires an agent.

## R12: an upgrade must not interrupt the fleet

Dibs is for agents that run for days. That makes the cost of an update a
product decision, not an operational detail: an operator who watches one
upgrade break a running fleet will stay on an old build to avoid repeating it,
and a coordination service that everyone is afraid to update is worse than one
that is briefly unavailable.

What a restart actually costs was measured rather than assumed. **State is never
at risk**: `state == fold(ledger)`, so a restarted daemon *rebuilds* the board
from the log rather than losing it. Replaying 139 ops took 4-8ms, and shutdown
already drains in-flight requests. The window a client can see is therefore
short, and the thing that hurt was not its length: it was that nothing survived
it. A call in flight died with `connection refused`, and an open
`subscriptions/listen` stream ended silently, which for a subscriber is
indistinguishable from nothing having happened.

Requirements:

- **An upgrade must not require agents to re-register, re-declare, or resume by
  hand.** The ledger already guarantees this; anything that breaks it is a bug
  in the same class as a lost op.
- **A client must wait out the restart window rather than fail through it**,
  bounded, so a daemon that is never coming back still surfaces as an error an
  agent can act on instead of a hang.
- **Only a request that provably never arrived may be re-sent.** A refused dial
  means the daemon never received it, so re-sending cannot duplicate an effect.
  A timeout may have been received and acted on: it is an error, not a retry.
  This is the whole safety argument, and it is what keeps "survive an upgrade"
  from meaning "silently duplicate work".
- **A subscription is re-established by re-issuing the caller's own request**,
  never by reconstructing what we think it wanted. The harness decided what it
  subscribed to; a restart does not make that our decision.
- **Downtime must stay proportional to replay.** Replay is milliseconds on an
  ordinary board. If a board ever grows to where it is not, that is a defect to
  fix here (a snapshot, or a warm handover that replays before taking the write
  lock), not a cost to pass to the fleet.
- **Never let a user believe an update risks their state.** The fear is the
  failure: it is what keeps people on old builds. Say plainly that the board is
  the ledger and a restart replays it.

## R13: a result must claim only what the server knows

`board` appended "Shown to the human in the board panel." to every result. It
was appended unconditionally, by a function that was not passed the answer, so
on a host that renders no panel the agent was told its human had been shown the
board. The agent then repeats that in its own words, and the human is looking at
an empty thread being told they were shown something. An agent cannot correct
for a result that lies to it.

The same defect has a quieter form: `detail=true` was honoured only in
`content`, which is exactly the carrier a `structuredContent` host drops, so the
one documented way for an agent to read the board returned a summary and the
agent's own token. That one is worse than a missing feature, because the agent
learns to route around the tool: it went back to querying the daemon over plain
HTTP and would have kept doing so.

Requirements:

- **A result states what the server did, not what it hopes happened elsewhere.**
  Where the outcome depends on the host, report what was sent and what the host
  declared it can render.
- **Uncertainty is not licence to claim the negative either.** The reference host
  declares nothing and renders anyway, so "not shown" would be its own false
  claim. Say what is known.
- **A declared parameter must take effect on every carrier the host may choose.**
  A flag that works only on the carrier this host discards is indistinguishable
  from a flag that does nothing, and the schema is the only thing an agent can
  see.
