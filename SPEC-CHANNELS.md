# Dibs (Spaces) SPEC v1.2. IMPLEMENTED

Extends SPEC.md §6, §8, §9. Nothing here changes v1 semantics; §18's scope
freeze holds, and spaces are inert until `dibd -match-repo` is passed.

## Implementation status

| Section | Status |
|---|---|
| §2 model, members, subscribers, public by default | **built** |
| §3 auto-join on `declare`, notify band, explicit join | **built** |
| §3 opening the first agent when nothing matches | **built**: was the missing half: matching only compared against agents that already existed, so on an empty board two agents declaring identical work were both told they had the field to themselves |
| §3 matching on `claim` as well as `declare` | **not built**: `claim` declares a path, `declare` declares the work; only the latter is scored today |
| §7 `scorer`, `announce_retry`, `announce_max_retries`, `space_exclusive_default`, `subagent_inherit` as CONFIG KEYS | **not built**: the behaviours exist and match the documented defaults; the keys do not, and `dibd` refuses to start on an unknown one |
| §4 Scorer interface; tier 0 (paths + git co-change) | **built** |
| §4 tier 2 embedding sidecar / tier 3 hosted | **built**, client (`dibd -match-embed-url`) *and* the sidecar itself (`contrib/embed-sidecar/`, MLX + F2LLM-v2-4B, measured best of four, see that README) |
| §4 tier 1 director-agent scorer | **withdrawn**: see below |
| §4.3 recorded score/prediction, replay contract | **built + proven** |
| §5 exclusive agents, queue, promotion on departure | **built** |
| §5.1 guard interaction | **built** (the guard predates this doc) |
| §6 `post` / `announce`, ack, redelivery via the wake path | **built** |
| §6 `announce_retry` throttle (120 s, ephemeral) | **built** |
| §6 `announce_max_retries` → mark `unacked` | **built** (5 tries, then marked and surfaced, never dropped) |
| §7 configuration | **built**, `dibd` flags and a `[match]` table in `dibs.toml`; flag > env > file |
| §8.1 director role | **built**, as the existing `coordinator` role scoped to spaces: `unlock_space`, `evict`, `merge_spaces` |
| §8.1 `director_required` gate | **built**: `dibd -match-director-required`; matches become `awaiting_director` and `admit` is the approval |
| §8.2 subagent inheritance | **built**: `parent` on register; membership, speech and departure all resolve through it |
| §9 `dibs calibrate` | **built** |
| §10 honesty rules | **built**, 1–6 |

Tests: `internal/core/channel_test.go`, `internal/overlap/overlap_test.go`,
`internal/mcp/e2e/channel_e2e.ts` (96 checks over real HTTP, including the
auto-join loop end to end against this repository's own history), and three
replay gates in `internal/ledger`,
`TestRandomizedReplayEquivalence` (1,500 fuzzed ops, reproducible across seeds),
`TestChannelReplayDeterminism`, and `TestDirectorReplayDeterminism`.

The fuzzed gate asserts only the ops a random walk reaches DEPENDABLY. The ones
needing specific preconditions, `ack_announcement`, `lock_space`, and the three
director ops, are pinned deterministically instead. That split is not a
preference: adding a coverage assertion revealed five space ops that had never
once been accepted while the gate reported "space coverage" and passed.

**Go naming is NOT renamed, and this is now a decision rather than a backlog
item.** `core.Agent` is the agent, `core.Space` is the agent, and the protocol
names (`open_space`, `join_space`, …) are already correct.

The rename was attempted and reverted. It cannot achieve what it is for, because
of a constraint §1 itself imposes: **the ledger's wire names are frozen**, so
`Op.Agent` and `Event.Agent` (tagged `json:"agent"`) must keep meaning the AGENT
forever. Renaming the Go type to `Agent` while the serialised field next to it
still says `agent` and means an agent leaves the codebase *more* ambiguous than
it started, not less, and the attempt collided `Op.Agent` with the existing
`Op.Agent *AgentInfo` on the first pass.

So the ambiguity is contained instead of moved: it lives in two type names that
`internal/core/space.go` documents at the top of the file, rather than being
spread across every JSON payload. Revisit only if the ledger format is ever
allowed a breaking version, which SPEC §4 does not currently permit.

That constraint is now ENFORCED rather than trusted.
`internal/ledger/wireformat_test.go` pins every on-disk field name and every op
kind string against a literal list, in both directions: an unexpected key and a
vanished one each fail with a message explaining the consequence.

It exists because nothing else could catch this. Every other test writes and
reads with the same code, so a renamed tag is invisible to all of them: the new
name is written, the new name is read, everything passes, and every ledger
written before the change stops replaying. `dibs verify` still reports the
chain intact, because the hash chain protects the LINES, not the meaning of the
keys inside them. The compiler cannot help either: both spellings compile.

Mutation-tested against the exact careless rename (`json:"agent"` → `json:"agent"`
and `OpLaneJoin = "channel_join"`); both fail loudly. Anyone revisiting this
decision now gets stopped by the gate instead of by a user whose board will not
open.

## The human is a participant

The board was a window: the operator could watch the fleet and could not speak
to it. Every affordance existed for agents, and the web board's own test
asserted "the operator view offers no actions": coherent for a monitoring tool,
wrong for a coordination service, because the human is the one participant who
always has context the agents lack.

The human gets an **agent identity**, not an agent. They join the agents agents
open, post and announce in them, send messages, and answer questions. An agent of
their own would be a room with one person in it.

**And they need not be a participant at all.** The identity is minted by the
first ACTION, never by loading the page: watching the board must not put an
agent on the roster, count you in the fleet, or subject you to liveness sweeps.
An operator who has joined nothing owes nobody an acknowledgement. The board
says "observing" until you do something.

The same is true of agents. Not every agent does development work: a monitor, a
reporter, a reviewer waiting to be summoned. `watch_space` gives an agent's
traffic without membership, and **only members are ever obliged to acknowledge
an announcement**; being nagged about work you are not doing is how a fleet
learns to ignore announcements. An agent may hold no agent membership at all.

**An agent somebody opened outlives its members.** Emptying it does not destroy
it, so a standing agent, "release", "security review", is normal, and agents
register and deregister as they come and go. The next arrival finds the same
agent with its accumulated topic and traffic, not a fresh one.

**An agent LANES opened does not.** When a declaration matches nothing, an agent is
opened for it automatically (§3), and those are reclaimed once they are empty,
unqueued and owe nothing. The distinction is not a nicety: the cap on agents is
generous for the ones a human chose to create and is exhausted within a day by
one per declaration, after which every later declaration silently gets no agent
at all. Applying either rule to both kinds breaks something real: reclaiming
everything deletes the standing agent an agent means to come back to, reclaiming
nothing leaks until the board is full.

Everything routes through the SAME ops an agent sends, with the human's own
token. There is no privileged write path: a parallel set of admin endpoints
would be a second authorization surface into the state machine, unledgered
unless each one remembered to ledger, and invisible to `dibs verify`. An agent
answering the human cannot tell (and need not tell) that it is talking to a
person.

Authentication is inherited, not invented: the routes sit behind the same
session cookie as the board, mintable only by proving the admin password.

## Scoring across tiers is not comparable raw

Tier 0 counts matched terms: a file sharing none genuinely scores **0**, so
renormalising against the maximum is correct.

Cosine similarity has no such floor. Two unrelated English texts embed around
0.3–0.7 with a modern model, and nothing lands near zero, so the raw value is
not the signal. **"How much better than typical"** is.

Renormalising an embedding prediction against its maximum therefore destroys
the only information it carries: chunks at 0.70 and 0.83 become 0.84 and 1.00.
Measured on a three-file fixture, `"writing release notes for the changelog"`
scored **0.729** against an authentication agent: a false positive confident
enough to put every agent in one agent.

A tier-2 prediction is rescaled against **the query's own similarity
distribution** before aggregation: the 25th percentile maps to 0, the maximum to
1, and anything at or below typical is dropped outright. Weak evidence here is
no evidence.

| probe (auth agent) | raw | rescaled |
|---|---|---|
| fixing the retry backoff | 0.803 | 0.595 |
| rate limiting on inbound requests | 0.830 | 0.693 |
| validating bearer tokens | 0.746 | 0.696 |
| restyling the board CSS | 0.402 | **dropped** |
| writing release notes | 0.729 | **dropped** |

The percentile is measured, not chosen by taste: the median was tried first and
zeroed true matches on a small corpus, where a query may genuinely relate to
most of it. Recall on a real 121-file repository was unchanged by the rescale
(MRR 0.666 before and after), so the false positives cost nothing to remove.

## A deadline must scale with the work asked for

One flat timeout treats a one-word probe and a 64-chunk batch as the same
request. Measured: both 4B models failed on `chunk 0/449`: the very first batch, having succeeded at the same batch size on an idle machine. A production host
under load hits this first, and `context deadline exceeded` names no knob.

The encode deadline is now `base + n × allowance`, bounded, and the error names
the fix. Timing out mid-index is worse than being slow: a half-built index
silently matches nothing, which is indistinguishable from a quiet fleet.

A related trap, worth recording because it is invisible: a **zero** timeout used
to be harmless, because `net/http` reads a zero `Timeout` as *no timeout*. As a
context deadline it means *already expired*, so every request fails instantly
with a message blaming a slow model. Zero now means "use the default".

## An announcement that gave up must get louder, not quieter

§6 marks an announcement `unacked` once redelivery exhausts its retries, and the
constant's own comment promised it "stays visible, never dropped". It did not:
the board counted only `open`, so an announcement **vanished at exactly the
moment it became interesting**: somebody was told something with collision
risk, never acknowledged it, and Dibs had stopped asking.

The two states are now separate numbers, and never folded into one:

- `unacked_announcements`, Dibs is still asking. Nothing to do.
- `abandoned_announcements`, Dibs gave up and nobody answered. **Only this one
  needs a person**, because nothing else is coming for it.

It is also the only filled mark on the board. Every other state is an outline,
because every other state resolves on its own.

## A guard that resolves nothing is inert, and only the daemon can see it

The guard fails open when it cannot resolve the caller to an agent. That is
correct, blocking every editor it cannot identify would be a broken editor
rather than a safe one, and it means a guard wired wrong is
**indistinguishable from a board where nothing is claimed**: every call allows,
every test passes, the fleet is unprotected.

The daemon is the only party that can tell, because it sees every call and
whether it resolved. Two counters make it diagnosable:

- **resolved 0, unresolved > 0** → hooks are wired and not one names an agent
  this board knows. This is the failure that cost a day: a plugin sending its
  own session id while the bridge registered the agent under another.
- **both zero** → nothing is asking; the plugin or hook is not installed.

`dibs doctor` reports this as a PROBLEM, not a warning, because a guard that is
running and protecting nothing is worse than one that is off.

## An allow must say which allow it is

`guard_path` returned a bare `{"decision":"allow"}` in two unrelated cases:
nothing claimed the path, and **the session could not be resolved to any agent
at all**. The second is the guard failing open, correctly, since blocking every
editor it cannot identify would be a broken editor rather than a safe one, but
it protects nothing, and it looked exactly like a clean board.

That is not hypothetical: a mismatched session id made the guard silently inert
for a day, while every test passed and the board looked healthy.

An allow now carries its `basis`: `no-claim` with the resolved agent, or
`unidentified-session` with a hint stating outright that it is **not** a finding
that the path is unclaimed.

## Things done TO an agent must reach it

An agent that declares work under a director gate is told
`action: "awaiting_director"`: and, until this was fixed, then told nothing
ever again. It was admitted seconds later with no way to learn the wait had
ended short of polling the event stream on the off-chance. The same held for an
agent promoted from an exclusive space's queue, and for one a director evicted:
still believing it held the agent.

All three are changes the agent **did not cause and cannot predict**, and all
three were silent. Normative: such a change is delivered through the wake path,
once, with what the agent may now do. "you may start; read the agent first",
"stop work there and coordinate before resuming".

Self-service actions are excluded deliberately: repeating your own tool result
back to you is noise. The distinguishing mark is in the event. `admitted_by`
or `from_queue` mean somebody else moved you.

## Silence is never an answer

A coordination service that fails quietly is worse than one that fails loudly,
because the failure looks exactly like success. `declare` returned
`{"ok":true,"slot_id":"s1"}` whether matching was **off**, still **indexing**,
**degraded** to the built-in scorer, or **working and genuinely found nothing**,
four unrelated situations, one identical reply, and the only way to tell was the
daemon's log, which agents cannot read.

Normative from here:

0. **"I found nothing" and "I have no opinion" are different facts.** Tier 0
   matches declared words against file PATHS, so a declaration about "token
   validation" in a repository whose file is called `auth.go` predicts nothing
   and can be compared against nothing. Reporting that as "you have the field to
   yourself" is a confident claim built on no evidence. The phase is
   `no-opinion`, and the hint says outright that it is **not** a finding of
   working alone.
1. **Every declaration reports why.** `matching` names the phase and
   `matching_hint` names the ACTION. "declare again shortly", "run `agents
   calibrate`", "point -match-repo at a checkout with history". A diagnostic
   that only names the fault leaves the reader exactly as stuck.
2. **A feature that is off must not look like a feature that found nothing.**
   Off, indexing, degraded and ready are distinguishable at every surface: the
   tool result, `GET /api/match-status`, the board's empty state, and
   `dibs doctor`.
3. **Every failure sets a status, not only a log line.** A scorer that switched
   itself off because the repo was not a git checkout must say so where the
   person and the agent will actually look.
4. **`dibs doctor` exists for the failures that are invisible by nature**: a
   stale harness secret is the worst: the harness starts fine, reports nothing,
   and simply has zero Dibs tools.

## 0. The problem this exists for

Directory claims (SPEC §9) detect exactly one kind of collision: two agents
naming the same path. That is the collision that is *cheap to detect*, not the
collision that hurts most.

Two agents refactoring the same concept, one in Go, one in TypeScript, never
name the same path and destroy each other's work anyway. An agent adding rate
limiting and an agent fixing a retry loop are unrelated in English and are the
same work in a codebase where both live in one middleware chain. Path overlap
cannot see either case, and no amount of claim discipline will make it.

Today the judgement is left to each agent: read the board, decide whether
somebody else's slot text sounds like yours. That is inconsistent by
construction: every agent applies a different standard, and the one that
judges wrong is the one that never looked.

**A space is one universal answer to "is this the same work?", computed once,
by Dibs, on the same evidence for everybody.**

## 1. Terminology. `agent` and `agent`

v1.2 splits a word v1 overloaded.

| Term | Is | Was called |
|---|---|---|
| **agent** | a participant: an identity, a mailbox address, a heartbeat, a token | "agent" |
| **agent** | a space of work that agents join |, (new) |

Agents work in agents. Everything that resolves *identity*, mail addressing
(§8), liveness (§7), the awareness gate (§6), the claim guard: keeps keying on
the agent and is unchanged.

**Migration is a rename, not a re-model.** The v1 `agent` object becomes `agent`
with the same id space, the same tokens, and the same ledger ops. Existing
ledgers replay unchanged: the op kinds keep their wire names (`register`
stays `register`) so no ledger rewrite is required or permitted. Tool
aliases are kept for one minor version and the board renders both.

## 2. The model

A **agent** is: an id, a human-readable topic, a set of member agents, an
optional owner, an optional queue, and a scoring provenance record (§4).

Three ways an agent relates to an agent:

| Relation | Sees traffic | Counted for collisions | Needs permission |
|---|---|---|---|
| **member** | yes | yes | if the space is exclusive |
| **subscriber** | yes | no | never (public agents) |
| **none** | no | no | n/a |

Dibs are **public by default**: any agent may read a public agent's traffic and
membership without joining it. Joining is the act that asserts "I am working
here", and only membership creates a collision.

Reading is encouraged and cheap. An agent that joins an agent SHOULD read its
recent traffic first; an agent's whole value is the context it already holds.

## 3. Declaring work, and auto-join

An agent declares what it is doing exactly as it does in v1 (`declare`. That
declaration is the query.

On `declare`) and, once implemented, on `claim`: Dibs scores the declaration
against every live agent (§4) and:

- **score ≥ `join_threshold`** → under the default `auto_join=declared` the agent
  is **proposed** (`action: consider`) and the agent decides. Only a shared
  identifying ref joins automatically: a score names a resemblance, an
  identifier names a thing. Under `auto_join=always` the agent is **auto-joined**
  on score alone, and told which
  agent, what the score was, and what drove it.
- **`notify_threshold` ≤ score < `join_threshold`** → the agent is **told**
  about the agent and may join. No membership is created.
- **score < `notify_threshold`** → nothing. If no agent matched at all, a new
  agent is opened with the declaration as its topic.

Both thresholds are human-tunable (§7). Auto-join is what makes the model
useful: a mechanism that only advises reproduces the v1 problem, where the
agent that needed the signal is the one that ignored it.

**An agent may always create, join, or leave an agent explicitly.** Scoring is a
default, never a cage.

## 4. Scoring: pluggable, tiered, and replayable

### 4.1 The interface

One interface, four implementations, chosen by configuration:

| Tier | Scorer | Dependencies | Available |
|---|---|---|---|
| 0 | paths, directories, **git co-change** | none | always |
| ~~1~~ | ~~director agent judges~~ | n/a | **withdrawn** |
| 2 | **local embedding sidecar** | one process | `-match-embed-url` |
| 3 | hosted embedding endpoint | network | `-match-embed-url` (same contract, different URL) |

Tier 2 and tier 3 are the same code and differ only by URL. **Dibs owns the
index, the chunking and the similarity maths**; the service is asked for one
thing, and that thing already has a universal API:

```
POST {base}/v1/embeddings   {"model": …, "input": ["…", …]}
                         →  {"data": [{"index": 0, "embedding": [...]}, …]}
```

Ollama, vLLM, text-embeddings-inference, LM Studio, llama.cpp's server and every
hosted provider speak exactly this, so **Dibs ships no service and invents no
protocol**.

This replaced a bespoke `/predict` endpoint that took a declaration and returned
repo paths, served by a sidecar we shipped. That design was incoherent and the
incoherence is worth recording, because the shape is tempting: it claimed to
accept any inference service while speaking a protocol no inference service
implements, so the only thing it could ever point at was ours, which we then
treated as foreign, configuring it by URL with no lifecycle and no
authentication. It also duplicated the indexing Go already does for tier 0.

The boundary that holds is the one operation Dibs genuinely cannot perform
in-process: turning text into a vector needs a model, and a model needs a runtime
we will not link. Everything else stays in Go.

Normative rules:

- **Weights are renormalised by Dibs** after retrieval, so a service returning
  distances rather than similarities cannot shift a calibrated threshold.
- **A file scores as its best chunk**, never its mean: a large file containing
  one highly relevant function is relevant, and averaging buries it under its
  own boilerplate.
- **Unreachable or slow is a DOWNGRADE, not an outage.** Matching continues on
  tier 0 and the provenance records that it degraded (§10.5).
- **Indexing happens once, at startup, off the request path.** Embedding a
  repository takes minutes; an agent declaring work must never wait for it.

### 4.2 Grounding: embeddings alone are not enough

A tier-2 scorer MUST NOT compare two task descriptions directly. Two tasks that
are unrelated in English embed as unrelated in English, which is precisely the
case §0 exists for.

The pipeline is:

```
repo indexed (symbols, files, docs)  ──►  vectors
agent's declaration  ──►  embed  ──►  retrieve the code regions it will touch
overlap of RETRIEVED REGIONS  ──►  expand one hop through imports + co-change
                              ──►  score
```

The comparison is between *predicted file sets*, not between sentences. This is
text-to-code retrieval, so a code-capable embedder covers both sides of the
query in one space and one model suffices.

### 4.3 The replay contract: normative

A similarity score is **impure**. Recomputing it next week against a reindexed
repository yields a different number, so a state machine that scores during
replay reconstructs different membership and the hash chain stops meaning
anything (SPEC §4).

This is the same problem as liveness, and takes the same solution (SPEC §2, §7):
**impure inputs arrive recorded in the op.**

> The `agent.join` op MUST carry `score`, `threshold`, `scorer_id`,
> `scorer_version` and the matched evidence. `Apply` MUST NOT invoke a scorer,
> MUST NOT read the filesystem, and MUST treat the recorded score as fact.

Replay therefore reproduces membership exactly, on any machine, years later,
without a model present. Three things fall out of this for free:

- **`dibs verify` keeps working** (the ledger is still fully deterministic.
- **Explainability**) "why am I in this agent" is a recorded number and a
  recorded reason, not a re-run.
- **Auditability**: changing the model changes future joins and cannot
  retroactively rewrite past ones.

## 5. Exclusive agents and the queue

The first member of an agent MAY declare it **exclusive**. This is the semantic
analogue of an exclusive directory claim (§9) and inherits its honesty rules.

While an space is exclusive, an agent whose declaration scores above
`join_threshold`:

1. is **told** who owns the agent, with the score and the evidence;
2. may **request** access, an ordinary `request` message (§8), so the existing
   approve/deny path applies unchanged; and
3. may **queue**, `queue_position` is returned, and the agent is auto-joined
   the moment the agent leaves exclusive.

The owner may grant, deny, or hand the agent over. Ownership ends when the owner
leaves the agent, or when its agent leaves `active`: identical to claim
expiry, and carrying the identical warning: **the coordination signal ended; it
is not proof the owner's processes stopped or that the work is safe to take.**

Queueing is what makes exclusivity tolerable. A blocked agent with a queue
position has somewhere to be; a blocked agent with a refusal has only the
option of ignoring it.

### 5.1 Interaction with the claim guard

Space exclusivity is advisory in the same sense claims are: until it is paired
with a path claim, at which point `guard_path` (SPEC §9) enforces it at the
edit boundary. The two layers are deliberately separate:

- a **space** says *this work is taken*;
- a **claim** says *these files are taken*;
- the **guard** is the only thing that stops a write.

An exclusive space whose owner also claims its paths is enforced. An exclusive
space with no claims is a strong social signal and nothing more, and MUST be
described that way to agents.

## 6. Traffic. `post` and `announce`

Two grades, because "everyone must know this" and "for the record" are
different needs and collapsing them trains agents to ignore both.

| | `post` | `announce` |
|---|---|---|
| Delivered to | members + subscribers | members |
| Acknowledgement | none | **required** |
| Redelivered | no | yes, until acked |
| For | FYI, progress, notes | anything with collision risk |

An agent's announcements **and posts** are READABLE on demand, by any current
member or subscriber, with `read_space`. This is not the same as being obliged by
them: an agent that joins after an announcement was made can see it and does not
owe an acknowledgement for it, and `read_space` labels each entry accordingly
(`OWED` / done / not required). Reading acknowledges nothing.

For a post, `read_space` is the *only* read path. The `agent.post` event says a
post happened, who, which agent, how many bytes, and never what it said,
because space events carry no recipient and anything in one is therefore
readable by every authenticated agent on the board, member or not (SPEC §10).
Posts are retained per agent (`post_retention`, default 128, oldest dropped
first) and are carried across a `merge_spaces` with the members who were
discussing them.

Without a read path, "you do not owe this" was implemented as "you cannot see
this": an agent's shared context was invisible to everyone who was not already in
it when it was said, and the notice sent to a newly-admitted agent told it to
read the agent while naming no tool that could. Membership is checked at read
time, so leaving or being evicted ends access immediately.

An unacked `announce` is redelivered to every non-dormant member every
`announce_retry` seconds, through the wake path (WAKE-MECHANISMS.md): the same
injection that delivers mail. This is why the injection mechanism matters: an
announcement nobody reads is worth nothing, and an agent mid-turn has no reason
to poll.

Redelivery stops on ack, on the member going dormant (it will see it on wake),
or at `announce_max_retries`, after which the announcement is marked
`unacked` and surfaced on the board. **visibly unresolved, never silently
dropped.**

Dormant members are not nagged. Waking an agent to acknowledge a message is
driving the harness, which Dibs does not do (PHILOSOPHY.md).

## 7. Configuration

| Key | Default | Meaning | Settable |
|---|---|---|---|
| `join_threshold` | *calibrated* | auto-join at or above | **yes** |
| `notify_threshold` | *calibrated* | mention between this and join | **yes** |
| `director_required` | `false` | all joins must be approved by the director | **yes** |
| `announce_retry` | 120 s | redelivery interval | no, fixed |
| `announce_max_retries` | 5 | then mark `unacked` | no, fixed |
| `scorer` | `auto` | highest available tier | no, selected from what is reachable |
| `space_exclusive_default` | `false` | first member takes exclusivity automatically | no, always false; pass `exclusive` to `open_space` |
| `subagent_inherit` | `true` | subagents inherit their parent's agents | no, always on (a vouched child inherits; see §8.2) |

The **Settable** column is not decoration. `dibd` rejects an unknown key and
refuses to start, deliberately, so a setting that was never going to take
effect cannot look applied, which means writing one of the "no" rows into
`dibs.toml` stops the daemon dead:

```
unknown setting(s) in dibs.toml: match.subagent_inherit: check the spelling
and the table they are under ([match], [limits]); nothing here took effect
```

This table previously listed all eight as though they were configuration. The
behaviours are real and match the defaults shown; only the keys are absent. The
full set the daemon accepts is `[match]`: `repo`, `join_threshold`,
`notify_threshold`, `director_required`, `deadline`, `embed_url`, `embed_model`,
`embed_query_prefix`, `embed_doc_prefix`; `[limits]`: `agent_ttl`,
`blob_store_bytes`; `[match]` also takes `history` (how many commits `calibrate`
and the co-change scorer read).

Thresholds are unitless and scorer-relative, which makes a shipped default a
guess. They MUST be calibrated per-repository from that repository's own git
history (§9), and the calibrated value MUST be shown to the human rather than
applied silently.

This is not a precaution, it is a measured requirement. Measured on five real
repositories with the tier-0 scorer:

| repository | files | recall@10 | recall@20 | MRR | calibrated `join` |
|---|---|---|---|---|---|
| agents | 121 | 0.488 | 0.653 | 0.542 | **0.327** |
| pi-mono | 1,142 | 0.229 | 0.320 | 0.261 | **0.055** |
| opencode | 6,330 | 0.191 | 0.245 | 0.152 | **0.063** |
| codex | 5,747 | 0.201 | 0.251 | 0.302 | **0.022** |
| hermes-agent | 7,435 | 0.250 | 0.304 | 0.287 | **0.024** |

**The calibrated threshold spans a factor of fifteen.** There is no default that
is not badly wrong somewhere: 0.327 auto-joins nothing on codex, and 0.022
collapses every agent in the Dibs repository into one agent. A scorer's absolute
numbers are a property of the scorer and the repository *together*, and neither
is known when the binary is built. This document originally proposed 0.75.

The second finding is why tier 2 exists. Tier-0 recall halves as a repository
grows, 0.488 at 121 files, ~0.20 at 6,000, because shared vocabulary dilutes
while the file count does not. Abstention stays near zero throughout, so the
scorer is not giving up; it is answering less precisely.

Measured head to head on identical cases, `dibs calibrate` against tier 0 and
then against real MLX sidecars. Full model comparison lives in
contrib/embed-sidecar/README.md; the short version is that **F2LLM-v2-4B wins on
every metric** (recall@10 0.638, MRR 0.780 at n=60) and that its public-benchmark
lead (which looked like contamination) survives on data it cannot have trained
on. Against the small model:

| metric | tier 0 | tier 2 | |
|---|---|---|---|
| recall@5 | 0.275 | **0.461** | +68% |
| recall@10 | 0.496 | **0.557** | +12% |
| recall@20 | 0.677 | **0.708** | +5% |
| MRR | 0.509 | **0.654** | +28% |

The gain concentrates where it matters: **recall@5 and MRR**, i.e. tier 2 puts
the right file near the TOP. A prediction is truncated before it is compared, so
being right at rank 30 buys very little; being right at rank 2 is the difference
between two agents meeting and not.

Note the calibrated `join` threshold also moved, 0.363 → 0.554. **Thresholds are
per-scorer as well as per-repository**: switching scorers without recalibrating
silently changes who gets auto-joined.

Small repository: tier 0 is genuinely enough. Large one, or one where the top of
the list matters: an embedding sidecar is where the accuracy comes from.

There is therefore no default to fall back to, by design: with no calibration
Dibs runs at `notify` only and says so, rather than silently guessing a bar it
cannot know.

## 8. Roles

### 8.1 Director

An optional agent holding the `director` role (SPEC §5 grant path: a human
grants it; no agent may promote itself). A director may: move agents between
agents (`admit`, `evict`), force-release agent ownership
(`unlock_space`), merge two agents (`merge_spaces`), and approve joins when
`director_required` is set.

Two powers listed here originally are **not built**: *read all agents* (a
director reads an agent by being in it, like anybody else. `read_space` is
members-only and there is no override) and *split an agent* (merge has no inverse;
open a new agent and move agents with `admit`).

`director_required` serialises the fleet behind one agent and is **off by
default**. When on, the director SHOULD auto-approve by policy and escalate
only on conflict; a coordinator that must think about every join is a
bottleneck wearing a coordinator's hat.

### 8.2 Subagents

Spawning subagents is ordinary agent behaviour and MUST NOT require
coordination ceremony. A subagent inherits its parent's agent membership,
including exclusive ownership, and does not join, queue or count separately.

The parent remains accountable: a subagent's traffic is attributed to the
parent's membership, and the parent's departure takes its subagents' access
with it. Inheritance is always on: there is no `subagent_inherit` key (§7), and
writing one stops the daemon. A parent that does not want a child to inherit
simply does not vouch for it: an unvouched child joins on its own merits.

**Lineage MUST be proven, not asserted.** `parent` arrives as a bare string and
anybody can type any name, so naming a parent grants nothing on its own. The
parent calls `vouch_child` with a one-time secret it generates, hands that value
to the child, and the child presents it as `parent_nonce` when registering. Only
then does it inherit anything. An unvouched claim of lineage is treated as an
ordinary stranger: it joins or queues on its own merits.

This is not belt-and-braces. Verified against a running daemon before the nonce
existed: an agent registering with `parent: "victim"` posted into the victim's
exclusive space, joined instead of queueing, and was handed allow/no-claim by the
guard for a path the victim held exclusively.

## 9. Calibration and evaluation: normative

A scorer MUST be evaluated before its thresholds are trusted, and the
evaluation MUST use the repository it will run against.

Git history is the ground truth and it is free: **a commit message is a task
declaration, and its changed file set is the label.**

```
sample N commits → message as query → measure recall@k over changed files
```

This measures the thing that matters ("did we predict the files this work
touches") on the user's own code, in their languages and conventions. It is
also **contamination-proof by construction**: no published model has trained on
a private repository's history, which no public leaderboard can claim.

`dibs calibrate` runs this, reports recall@k per scorer, and proposes
thresholds. It is a permanent regression test: when a better model appears it
is measured in an afternoon rather than argued about.

## 10. Honesty rules: normative for all surfaces

1. Semantic overlap is a **heuristic**. A high score is evidence, not proof; a
   low score is **not** proof that two agents will not collide.
2. Agent membership is a coordination signal. It does not restrain any process,
   and only the guard (§5.1) stops a write.
3. Every auto-join MUST be explainable on demand: score, threshold, scorer, and
   the evidence that drove it.
4. Auto-join MUST be reversible by the agent and by the human, always.
5. A scorer that degraded MUST say so. A tier-0 score presented as a tier-2
   score is a lie about how much is known.
6. An unacked announcement MUST remain visible. Silence is never resolution.

## 11. Open questions

- ~~Agent merging when two agents drift into the same work~~. RESOLVED: always a
  director decision (`merge_spaces`), never automatic. Merging is destructive to
  context, and a similarity threshold is the wrong thing to trust with it.
- Should `join_threshold` be per-agent rather than global? An agent covering a hot
  file may deserve a lower bar than one covering docs.
- Cross-repository agents: an agent in `~/api` and an agent in `~/web` working
  one feature share no paths and no history. Probably needs an explicit link,
  not a score.
- ~~`director_required`~~. RESOLVED: built, and OFF by default. §8.1's own
  warning stands, so the flag exists for fleets that want the gate and nobody
  is opted into it silently.
- Does an exclusive space imply an automatic path claim on its retrieved region?
  Convenient, and a large widening of what a single call does.
