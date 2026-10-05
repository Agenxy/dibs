# Dibs (Specification v1.1) LIVING (committed, not frozen)

A self-hosted coordination and situational-awareness service for concurrent AI agents.
Agents **register** themselves, **declare** what they are working on, exchange typed
messages through private **mailboxes**, transfer files as content-addressed **blobs**,
place advisory **claims** on resources, and gather in **spaces**. No agent can act on
another through the system: the worst you can receive is a message you may decline.
Dibs is a visibility layer, not an orchestrator.

"Local" means self-hosted, not single-machine: one `dibd`, one data directory, no
external service. It listens on loopback by default and serves agents on other
computers when bound to a tailnet or LAN address. Federation *between* daemons is not
specified here (see issue #12).

**What Dibs is actually for.** The failure it prevents is *redundant effort*: two
agents independently pursuing the same objective (see REQUIREMENTS.md: three PRs, ~3,900
diff lines, one goal). It is NOT a mutex over source files. Concurrent edits to the
same file are normal and healthy; version control solved that, and suppressing it would
destroy the parallelism that makes a fleet worth running. Claims are a *communication*
primitive, "I am here, coordinate with me", and only the rare `exclusive` mode over
resources git does not isolate (a local install, a dev server, a device) is a real
request for exclusion.

Design creed: simple, rigorous, bounded, and **honest**: every guarantee stated here is
one the system can actually enforce, and every limit of enforcement is stated with it.

**Status: living.** Committed and implemented, but deliberately NOT frozen: freezing
before end-to-end validation buys nothing. Change it whenever reality disagrees with it;
record why. Hardened by five adversarial external review rounds (12 → 12 → 7 → 7 → 4 → 2
findings; no P0 after round two; freeze confirmed round six with "no findings ≥ P1").
**The spec is the contract; code follows it.** Changes now require a revision
proposal and re-review of the touched sections.

Two different things are called frozen here and they are worth separating, because
a reviewer read them as a contradiction. This *document* is living: it is revised
when reality disagrees with it. What §18 freezes is the v1 *scope*, the list of
what v1 does and does not attempt, and what §12 calls a frozen contract is the
v1.0 tool table, kept unchanged because it is the surface those review rounds
examined. A living document can describe a fixed scope; neither statement licenses
changing the other.

---

## 1. Architecture

```
┌─────────────┐   MCP 2026-07-28 (streamable HTTP, POST /mcp)   ┌──────────────────┐
│ agent A     │──────────────────────────────────────────────▶  │  dibd (daemon) │
│ agent B     │──────────────────────────────────────────────▶  │  single thread   │
│ agent C     │──────────────────────────────────────────────▶  │  in-memory state │
└─────────────┘                                                 └───────┬──────────┘
      ▲                                                                 │ append + fsync
      │ agents CLI / web board (human view, decrypted)                   ▼
      └──────────────────────────────────────────────────  ~/.dibs/ledger.jsonl
```

- **`dibd`**: single-threaded event loop; all mutations and reads execute sequentially
  in one goroutine. No locks, no transactions, no deadlock by construction. Binds
  `127.0.0.1` only; single instance per data dir via `flock`.
- **State**: entirely in memory, **bounded** (§11 caps + §4 GC make replayed state
  bounded, not just current state).
- **Ledger**: append-only JSONL, fsync'd per record, replayed on startup (§4).
- **Transport gate**: every HTTP request must present the **local access secret**
  (§5): loopback TCP is reachable by *other OS users*, so loopback alone is not an
  authentication boundary. Browser `Origin` headers not on localhost are rejected
  (DNS-rebinding defense).

## 2. The state model and core invariant

State is partitioned into three tiers, and the tier boundary is normative:

1. **Replayable state**: agents, slots, messages, claims, nonces, dedup records,
   acked serials, status fields. Mutated ONLY by ledgered ops.
   > **Invariant: an op is ledgered iff it changed replayable state, every change
   > has exactly one serial, and unledgered activity never mutates replayable
   > state.** Replay is exact: `state == fold(ledger)`.
   `Apply` strips process-local monotonic readings from its supplied `now` before
   comparisons or storage: the clock in replayable state is the wall clock the ledger records.
2. **Engine-ephemeral state**: lease freshness touches (from reads/heartbeats), rate
   buckets, parked long-polls, the event ring. Never replayed, never trusted across
   restart. Ephemeral facts influence replayable state only by being **recorded as
   decisions inside ledgered ops** (sweep's `stale_agents`/`dead_agents`, wake ops).
3. **Presentation annotations**: `last_seen` (freshest activity incl. reads),
   `seen_source` (boot grace, authenticated contact, harness hook, ledger activity,
   other runtime activity, or none),
   `proc_alive`, and row `host` (one display label per known host identity;
   raw `agent.host` retained, selection described in docs/NETWORK.md §2).
   These appear in board/CLI/web *views*, computed live by the engine at
   read time. They are **not replayable state**, not in the agent's ledgered schema, and
   replay does not reconstruct them. (A "quiet sweep" therefore changes no state: it
   only refreshes annotations.)

**Ledgered wake transitions:** an agent's `status` is replayable state, so *nothing
unledgered may change it*: including reads. When an authenticated call (read or
write) arrives for a `dormant` or `stale` agent, the engine first commits a
**`wake`** op (event `agent.awoke` / `agent.recovered`), then serves the call.
A wake also **clears `acked_serial`**: each activation must re-pass the awareness
gate before `declare`/`claim` (§6).

**Wake phases (normative, which calls wake):** request processing has four phases:
(1) transport/auth, local secret + agent token; (2) structural validation: parse,
field bounds; (3) rate admission; (4) domain execution. A call that passes 1–3
**wakes the agent even if phase 4 rejects it** (`E_MUST_ACK_BOARD`, `E_NO_CLAIM`, …):
an authenticated, well-formed, admitted attempt is real liveness evidence. Calls
failing phases 1–3 (unauthenticated, malformed, rate-limited) never wake: they are
never ledgered and must not change state.

**Event observation is not turn activity.** `await_events`, `events_since`,
`recent_events` and subscriptions authenticate and rate-admit as before, but
do not wake a dormant agent, refresh its lease/coordination checkpoint, stamp
`last_seen`, or retract the session's recorded Stop. A watcher may wait between
turns without making the session look busy. Actual model calls and starting
hooks remain turn evidence; finishing hooks make the current session wakeable.

## 3. Serials and ordering

A single `u64` per node. One accepted mutating op = one serial = one ledger line.
Events emitted by an op share its serial with a `sub` index; total order is
`(serial, sub)`, and all events of one op are atomic (they exist iff the op's line
does). The serial is: sync cursor, awareness watermark, message identity, and dedup
key. It orders **API-visible coordination state only**: it cannot order or fence
filesystem writes (§9). It is a **coordination generation**, not a fencing token.

## 4. Ledger

```json
{"s":1042, "t":"2026-07-22T18:04:11.302Z", "n":"a1b2c3d4", "e":"send", "prev":"9f2c…", "op":{…}}
```

- **Command sourcing**: the ledger records ops (with timestamps and all recorded impure
  inputs); replay re-applies them through the pure core.
- **Hash chain**: `prev` = SHA-256 of the previous line's raw bytes (covers ciphertext;
  `dibs verify` needs no key). Snapshots (v1.1) anchor the head hash; `agents
  checkpoint` exports it.
- **Crash semantics (normative)**:
  - One op = one line = one fsync = the atomicity unit; multi-effect transitions are
    one op.
  - Order: apply in memory → encrypt/serialize/append/fsync → reply. **Any failure in
    the persistence step is fail-stop** (encrypt, serialize, write, and fsync alike):
    dibd exits rather than let memory diverge from disk.
  - **Indeterminate-commit window**: a crash after fsync but before the reply means a
    caller cannot infer "not committed" from a missing response. Mitigations, by op:
    `send` takes a client `op_id`; `register`/`resume` are keyed by
    nonce/`resume_id` (§5). All other mutations are naturally idempotent or safely
    rejected on retry (`respond` → `E_MSG_FINAL`, `claim` → renewal, `declare` →
    overwrite, `ack_*` → no-op). Semantics: **effectively-once for identified ops,
    at-least-once-with-safe-retry for the rest**: stated, not implied.
  - **Dedup records are bounded and payload-bound.** Each dedup record is
    `{op_id, digest, result-ref, activation}` where `digest` = SHA-256 of the
    normalized request; reusing an `op_id` with a different digest fails
    `E_OP_ID_CONFLICT`. **The dedup guarantee is the lesser of 24 hours and the
    agent's 256 most-recent identified ops**: beyond either bound the oldest record
    is pruned (deterministic sweep GC, independent of the referenced message's
    retention; the record retains what a retry needs) and retrying an evicted id may
    duplicate. At the full 10 ops/s all-identified rate, 256 records cover ~25 s,
    retries are a seconds-to-minutes affair, so the practical window is the cap
    only under sustained bursts, and the contract states both bounds rather than
    promising the larger. `resume_id` records follow the same bound (the 1/10 s
    resume rate makes eviction there a non-issue in practice).
  - Torn final line: truncated on replay (expected crash artifact, not corruption).
- **Encryption at rest**: message bodies, responses, and agent tokens sealed with
  AES-256-GCM under `~/.dibs/key` (0600). Public fields stay plaintext (`tail -f |
  jq` remains a live public board). Snapshots retain private bodies only as ciphertext.
- **Deterministic GC (makes replayed state bounded)**: sweeps prune, as pure functions
  of `(state, recorded now)`: terminal messages beyond per-agent retention (§11),
  archived agents (and their nonces and dedup records) past retention. Pruned data
  remains in ledger history; replay re-prunes identically.
- **Rotation. NOT IMPLEMENTED. Planned for v1.1.** The design is: at 64 MB / 1M
  lines, snapshot via temp-file write → fsync → atomic rename → directory fsync;
  new segment's first `prev` = anchored head hash.

  Stated this loudly because the `(v1.1)` tag it used to carry was easy to read as
  a shipped bound: a reviewer drove a scratch ledger to 68 MB, found one file and
  no segment, and reported the rotation as broken rather than absent. **Today the
  ledger is a single append-only file with no size limit**, so disk use and the
  daemon's startup replay both grow with the lifetime of the board. Nothing
  corrupts and `dibs verify` stays correct; it simply gets slower and larger
  forever. A long-lived board wanting a fresh start archives the directory and
  starts a new one.

Ledgered op kinds: `register, resume, wake, activity_checkpoint,
check_in, update, sign_off, heartbeat` (recovery only), `declare,
undeclare, send, respond, ack, claim` (incl. renewals), `release,
sweep` (only when it changed state), `mark_delivered`, `outcome_read`
(a durable outcome/review read prefix; historical full sender reads retain
their original meaning), `initialize_review_read` (one recorded upgrade
read cutoff; historical sender progress and recipient review units at or below
it are already read in the derived views, without changing historical folds
or discarding genuinely unread legacy verdicts and queue changes).
The unreleased cutoff op also snapshots each retained identity incarnation's
legacy awareness watermark, so later check-ins cannot turn an unquoted or
partly quoted old verdict into read evidence. Historical read ops are unchanged.

### 5.0 Agent identity is observed, never self-reported

Every descriptive field on an agent, `cwd`, `branch`, `model`, `harness`,
`session_id`, is also a tool ARGUMENT, which means a model *could* fill it in.
Models do not. Driving real harnesses against real models settled this:

```
dibs_register {"cwd":"","branch":"","model":"","session_id":"","pid":0,
                     "name":"oc-alpha","description":"opencode agent A"}
```

That is a live `gpt-oss-120b` run through opencode. Every observable field blank.
So the stdio bridge fills in what it can see for itself, and only ever fills a
field the caller left empty:

| field | observed from | available to |
|---|---|---|
| `host` | `os.Hostname()` | every harness |
| `cwd`, `branch` | `os.Getwd()` + `git symbolic-ref` | every harness |
| `pid` | the bridge process itself | every harness |
| `session_id` | the bridge process itself | every harness (fallback) |
| `harness`, `version` | `clientInfo` at initialize | every harness |
| `harness` | `DIBS_HARNESS` | any harness whose SDK will not say |
| `title`, `surface`, `session_id` | Claude Code's on-disk sidecar | Claude Code |
| `model`, `provider` | the pi extension, from pi's own argv | pi |

Two consequences were real bugs, both found only by running the harnesses:

- **`cwd` and `branch` were Claude-Code-only.** Every opencode, codex and hermes
  agent registered with no working directory: deleting the single most useful
  disambiguator on a fleet board. The bridge is spawned as a child of the
  harness and inherits its cwd, so `os.Getwd()` was always available.

- **`session_id` was never set, so reattach never fired.** Reattach keys on
  (name, session_id). Three consecutive runs of the same agent produced
  `oc-alpha`, `oc-alpha-2` and `oc-alpha-3`; a question sent to the second was
  invisible to the third. The agent's address changed underneath it, silently,
  and the board filled with ghosts.

  The fix keys on the bridge process. Harnesses spawn one stdio bridge per
  session and hold it for that session's lifetime: opencode's
  `MCP.connectLocal` passes no session identifier of its own, just `process.env`
  plus user config, so there is nothing else to observe. **This process is the
  session.** Re-registering inside it reattaches; a genuinely new session gets a
  new bridge and correctly gets a new agent. Verified both ways.

  The id is `bridge-<pid>-<random>`, not bare PID: PIDs are recycled, and a
  recycled PID would silently reattach a fresh session onto a dead agent and its
  mail.

- **`pid` was asked of the model, which cannot know it.** It drives the sweep's
  dead-agent detection, and arrived either absent (`0`, suppressing `proc_alive`
  entirely) or wrong: a live glm-4.6 run sent the literal string `"$$"`. The
  bridge process is the better answer regardless: it starts with the session and
  exits with it, so "is this pid alive" and "is this agent still connected" are
  the same question. The harness's own pid is not reachable, because harnesses
  wrap the bridge.

- **A client may announce its SDK instead of itself.** hermes connects with the
  official Python SDK and arrives as `{"name":"mcp","version":"0.1.0"}`, so its
  agent read `harness: mcp`: meaningless on a mixed fleet, and identical for
  every Python-SDK client. `DIBS_HARNESS`, set in that harness's own MCP server
  config, names it. A declared harness is used ONLY when the client's own name is
  a known SDK placeholder; a client that identifies itself always wins.

  Deriving this from the parent process was implemented and removed. Harnesses
  wrap the bridge, hermes under `tools/mcp_stdio_watchdog.py`, Claude Desktop
  under a `disclaimer` helper, so the parent is never the harness, and the
  heuristic produced "python" and "disclaimer".

**Where a value is measurable, observation OVERRIDES self-report.** The bridge
defers to what the agent typed, because for `model` it genuinely cannot know
better. The pi extension does not: pi's model is named on its own command line,
and a live run reported `model: "gpt-4"` while running `gpt-oss-120b`. A field
you can measure is never improved by asking.

## 5. Identity, authentication, and the security boundary

**Threat model (two rings, both stated):**

- **Other OS users on the machine**: kept out by the **local access secret**,
  `~/.dibs/local.secret` (0600, CSPRNG), required on every HTTP request
  (`X-Dibs-Local` header; cookie for the web board). Same-user agents and the CLI
  read it from disk; other users cannot. `dibs mcp-config` prints the host MCP
  config including the header. This is a *transport gate* (proves same-user), not an
  identity.
- **Same-user agents**: isolated from each other's agents/mailboxes by **agent tokens**, but only against *accidental* interference. A malicious same-UID process can read
  the key and the secret; that boundary requires OS isolation and is explicitly out
  of scope. Dibs' promise at this ring: honest agents cannot forge, snoop, or
  collide by accident.

**Credentials:**

- **Agent token**: 256-bit CSPRNG hex, returned by `register`/`resume`.
  Write + mailbox-read capability for one agent. Passed as a **tool argument**
  (normative; MCP hosts cannot vary headers per call). Constant-time comparison.
  Exposure to the owning agent's context is bounded-by-design: blast radius = its own
  agent.
- **Registration nonce**: client-generated, ≥128-bit CSPRNG, **expected for
  `kind: persistent`** and optional for ephemeral. A client that sends none is
  not refused: the daemon MINTS one and returns it, because an agent told to
  keep a credential it was never given can never recover, and every persistent
  registration therefore has one (§6). A minted nonce is weaker than a chosen
  one and the result says so: the row stays recoverable by name and session id,
  which a chosen nonce closes. Constant-time comparison. Two roles:
  - *Response-loss retry*: `register` with a nonce it has seen, while the agent is
    active and was created within one agent TTL, returns the original result
    (`resumed: true`). Outside that window: `E_NONCE_IN_USE` with hint → `resume`.
  - *Recovery credential* for persistent agents via `resume`. **Treat a
    persistent agent's nonce as a secret equal to its token.**
  - *Presented by the harness, on any transport.* An agent cannot carry a secret
    across a context boundary. Its context ends, which is the event the nonce
    exists for, and the nonce goes with it. So the nonce MAY arrive in transport
    metadata instead of as a tool argument, from the one place that outlives a
    context: the harness's own MCP server config.

    | transport | where |
    |---|---|
    | Streamable HTTP | `headers = { "X-Dibs-Agent-Nonce" = "…" }` |
    | stdio | `env = { DIBS_AGENT_NONCE = "…" }`, forwarded by the bridge |

    Accepted only for `register` and `resume`, the calls where `nonce` means
    "who I am". `vouch_child` also takes a `nonce`, and there it is a secret a
    PARENT issued for one specific child; filling that from the caller's own
    identity would let any agent vouch for a child it never spawned. An argument
    the agent supplies always wins; this only fills a blank.

    This is what makes the transport a free choice. Identity used to depend on
    the stdio bridge being a per-session process with a filesystem, so an HTTP
    client could not reattach at all, and "which transport" silently meant
    "whether reattachment works". 2026-07-28 removed connection-scoped sessions
    precisely so cross-call state travels as explicit handles rather than as a
    property of the pipe; this is Dibs taking that up.
- **`resume(nonce, resume_id, pid?)`**: the explicit activation op for standing
  roles. `resume_id` (client-generated per attempt, ≥64-bit) makes it a **complete
  activation boundary**:
  - Verifies the nonce (constant-time); fails on closed (`E_AGENT_CLOSED`) or
    unknown nonce (`E_BAD_NONCE`). **An `archived` agent resumes**: retention keeps
    its row, its mailbox and its nonce index entry for `archive_retention` so that
    it can, and the alternative the refusal used to advise (register a new agent)
    forks a sibling with an empty mailbox beside the one holding the mail. Only a
    row that retention has actually purged is `E_NO_AGENT`.
  - **Rotates the token** and increments the agent's `activation` generation: the
    rotation takes effect atomically at the resume op's serial: ops carrying the old
    token that execute after it fail `E_BAD_TOKEN` (all validation happens inside
    the single-writer loop at execution time, so there is no window), and **parked
    long-polls of prior activations are cancelled** at the same serial.
  - **Rebinds** `(pid, proc_start_time)`, wakes the agent, clears `acked_serial`.
  - **Durably idempotent per attempt, generation-aware**: a retry with the same
    `resume_id` returns the original result (including the rotated token) *iff
    the agent's activation generation still equals that attempt's*. If a later
    resume has advanced the generation, the retry returns
    `{superseded: true, activation: <original>}` **without** a token: recoverability
    never resurrects a credential that has already been rotated out.
  - **Exception to the general wake rule (§2), by design**: `resume` performs
    its own wake atomically inside the resume op: no separate `wake` precedes
    it. One activation = one serial.
  - **Resuming an active agent is legal** (take-over of a wedged or superseded
    activation: the standing-role reality) but rate-limited to 1 per 10 s per agent
    to bound rotation thrash; dueling *deliberate* resumers are same-user malice,
    out of scope per §5's threat model.
- **PID binding**: liveness signal only, never authentication (§7).
- v2: Ed25519 per-agent signatures; UDS peer credentials as an alternative transport
  gate.

## 6. Dibs, slots, and the awareness gate

**Agent**: replayable: `{agent_id, kind, name, description, pid?, status,
created_serial, acked_serial, activation, last_coordination_at,
stale_since?/dormant_since?, slots}`; presentation (view-only, §2): `last_seen,
seen_source, proc_alive`. Public; writable only by token holder. `agent_id` = uniquified name
slug. `activation` is a generation counter incremented by each `resume` (§5).
`last_coordination_at` is the agent's **latest durable coordination checkpoint**: a
conservative lower bound on its own last accepted authenticated call (it may trail
the true latest by up to TTL/2, the §7 coalescing interval). Updated only by
**ledgered ops in which this agent is the actor** (its own calls, register, resume,
wake; §7's `activity_checkpoint` refreshes it during ephemeral-only activity). Mail
*received*, probes, and other agents' ops never touch it: another agent cannot keep
an abandoned agent looking active.

**Kinds:**

- `persistent` (**the default** since v0.0.7: a register that names no kind is
  persistent, and one that sends no nonce is given one and returns it as `nonce`,
  the credential that reattaches it after anything), a **standing role** (reviewer,
  nightly maintainer) whose agent idles between activations.
- `ephemeral`, session-scoped, on request (`kind: ephemeral`). Status:
  `active | stale | closed | archived` (+ `unreachable` reserved for v2).
- A persistent agent's Status: `active | dormant | closed | archived`. Dormant is
  deliberately not "stale": it is *expected* sleep. The agent, description, slots, and
  **mailbox stay live through dormancy**: mail queues while the agent sleeps; the
  serial cursor + §10 checkpoint give retention-bounded catch-up on wake (§8: within
  bounds guaranteed, beyond them explicit and detectable). Claims still
  expire on their own leases (§9). Registration requires a nonce (§5); reactivation
  is `resume`.

**Review coordination:** a verdict-only review is a `question`, closed by an
`answer`; an approved `request` remains owed until its worker reports `done`.
Declarations sharing an objective retain that evidence in `overlaps`. Known
complementary activities (including implementation and review) explain the
relationship without a duplicate-work warning. Equal or unknown activities still
warn, and a peer's complementary slot does not hide its separate duplicate slot.
This is advisory presentation derived from the recorded activities, not a new
permission, admission rule or ledger field.

**Addressing: `agent_id` is the address; the `name` is one too.** Everything the
ledger records about an agent names it by `agent_id`: mail `to`/`from`, claim
owners, space memberships, role pins, the nonce index. So `agent_id` is
**immutable**, and `update(name=…)` moves the label only. The name is *also*
accepted wherever a call names an agent (`send(to=)`, `grant_role`,
`prune`, `force_release`, `adopt_agent`, `admit`, `evict`, `merge_agents`,
`queue_lock`, and authorized `all_mail(agent=)`),
because until then it was not, and a board that publishes a name as an agent's
identity while refusing it as an address makes **discovery and addressing
disagree**: an agent that renamed itself on changing role published an address
that reached nobody. Resolution:

0. A **role address wins over everything**: `to: "human"` is the person and
   `to: "coordinator"` whoever holds that role, on `send`, even if some agent
   has taken either word as its name.
1. An **exact `agent_id` wins outright**, whatever state that row is in. Every
   call that resolved before this existed resolves to the same row now, and a
   namesake can never overtake an address.
2. Otherwise the **name**, preferring rows that are not closed or archived, so a
   retired row never shadows the agent that took its name over.
3. A name **two eligible rows hold is refused** (`E_AMBIGUOUS_AGENT`, hint names
   the ids), never guessed: Dibs does not pick a mailbox on a caller's behalf.
4. A reference matching nothing is passed through unchanged, so the refusal the
   caller reads is the one that names the nearest live agents.

Resolution happens **at ingress**, and the op reaching the ledger carries the
id, exactly as `to: "coordinator"` has always been resolved: a name moves, so a
ledger recording one could replay into a delivery to whoever holds that name
later. The fold is unchanged, so `state == fold(ledger)` holds for every ledger
written before this. Consequences an agent can rely on: mail addressed to an
`agent_id` **reaches the same row before and after any rename**, because the id
never stops being the row's key; a rename onto any other row's `agent_id`, or
onto another live agent's name, is refused (`E_NAME_TAKEN`) at admission.
Historical renames still fold, and an unchanged historical label may be kept.
The old *name* stops addressing you unless it is also your ID, which
`update` says in its result so you can tell whoever was waiting.

**Awareness gate**: before `declare` or `claim`, an agent must have called
`check_in()` **with its current credential**: the gate re-arms exactly when the
token rotates, which is when a new session takes the identity (`register`,
`reattach`, `resume`; §2). Pre-ack writes fail `E_MUST_ACK_BOARD` (hint names the
fix). A new session cannot mutate the board on its predecessor's awareness.

*Revised 2026-09-30.* The gate used to re-arm on every dormant/stale transition as
well, to stop an agent that slept for a month acting on month-old awareness. That
tied awareness to the board's liveness guesses, and whenever a guess was wrong it
locked a live agent out: a process restart misread as a crash got the agent swept,
woken by its next call, and swept again, each step erasing its acknowledgement, so
`declare` was refused indefinitely under a hint to call `check_in`, which it had just
done. A sweep and a wake keep the token, so they keep the acknowledgement. The
cost is the month-asleep case, accepted because declaring and claiming are advisory
(rule 4) and visible, while a gate that fails closed on a wrong guess stops a live
agent from saying what it is doing. The operator's rule behind it: an agent that is
not archived is live.

**Store-and-catch-up, and since v0.0.7 a wake.** Mail to a dormant agent waits for
the agent's next activation, and the board may bring that activation about: an
operator's `[wake.exec.<harness>]` command (argv from `dibs.toml`, one fixed
sentence, rate limited, confirmable by exit status) runs when mail somebody is
blocked on arrives for an agent that has stopped, and a harness session socket,
where the harness publishes one, is tried best-effort. Both carry "you have mail"
and nothing else: the board wakes an agent and does not steer one. See
`WAKE-MECHANISMS.md` §5 and §5b.

## 7. Liveness: three signals, honestly labeled

| Signal | Mechanism | PROVES | Does NOT prove |
|---|---|---|---|
| `dead` | `kill(pid,0)` per sweep + (pid, start-time) identity | The registered process is gone. Caveat: an unreaped zombie still *appears alive* to `kill(0)`; true zombie detection arrives with kqueue `NOTE_EXIT`/pidfd (v1.1) | That its children or in-flight effects stopped |
| `stale`/`dormant` | Lease lapse: no model contact for `idle_ttl` (default 45 min); `agent_ttl` (default 5 min) applies only to a recorded PID with no configured prober | The agent stopped *coordinating* | That it stopped *working*, `stale + proc:alive` renders as "hung?", a hint, never a verdict |
| `expired_unanswered` | Deadline passed, recipient active | This message wasn't answered | Anything about recipient health |

- **Implicit heartbeat**: authenticated model calls (reads included) refresh the
  ephemeral lease. Explicit `heartbeat` is for otherwise-idle agents; ledgered only
  when it wakes/recovers an agent.
- **Fresh identity contact beats a stale recorded PID.** A successful token-authenticated
  model read or mutation proves that identity is present, even if the process it
  registered from has gone. The override uses the existing `idle_ttl`, is fenced
  to the agent incarnation, and never reports the old process as alive. Registration,
  boot grace, event polling and background subscriptions do not create this evidence.
  A bridge's token-authenticated inbox read counts too; this is not limited to
  model turns. Tokenless lifecycle `hook_poll` retains its existing hook evidence
  and does not create this override.
  When it expires, the dead PID again establishes a crash. Evidence is local to the
  running daemon; after restart, existing boot/checkpoint rules apply and a new
  authenticated model call is needed for this override. Process/session metadata
  is not refreshed or rebound by this rule.
  A member that crashes immediately after qualifying contact can consequently
  keep an announcement outstanding, rather than blocked, for up to `idle_ttl`
  (45 minutes by default). Once contact expires, normal crash detection and the
  blocked-announcement diagnosis resume at the next sweep.
- **Seen evidence carries its origin.** Boot grants grace from a recent durable
  checkpoint; it is not an observed call. `seen_source: "boot_grace"` labels that
  timestamp explicitly, and the CLI says "boot grace" rather than "seen".
  Actual token contact becomes `authenticated_contact`; later lifecycle hooks
  become `harness_hook`. A replayed checkpoint without a newer observation is
  `ledger_activity`, and other runtime stamps are `activity`. Losing ephemeral
  provenance falls back to the durable evidence, never invents a client call.
  These labels do not change the timestamp, lease, sweep or wake decision.
  Process/session refresh remains a separate design-only proposal in
  [docs/PROCESS-REFRESH-DESIGN.md](docs/PROCESS-REFRESH-DESIGN.md).
- **Sweep decisions are recorded** (`stale_agents`, `dead_agents`, `alive_pids`),
  replay applies decisions, never re-probes (§2). Quiet sweeps are unledgered.
- **Lifecycle clocks run from ledgered transitions, not ledgered activity.** The
  sweep that marks an agent `stale`/`dormant` is a ledgered op recording
  `stale_since`/`dormant_since` as replayable state; the archive clocks (30 min
  grace, 30 d dormancy max) run from **that recorded transition**. Consequences,
  both directions: an *active* agent never ages toward archival no matter how quiet
  its ledger is (ephemeral reads/heartbeats keep it active; there is nothing to
  age), and a restart cannot fast-forward archival either, because an agent is
  archived only ≥ grace *after a ledgered transition that replay reproduces*.
- **Coalesced activity checkpoints make boot decisions evidence-based.** Purely
  ephemeral activity (reads, heartbeats) leaves no ledger trace, so when an accepted
  authenticated call **by an agent** arrives and that agent's `last_coordination_at`
  (§6: its replayable own-activity record) is older than TTL/2, the engine ledgers
  a tiny **`activity_checkpoint`** op whose state effect is precisely to set
  `last_coordination_at` to the op's timestamp: at most one line per agent per
  2.5 min, only while active-but-quiet, and satisfying the §2 invariant (a ledgered
  op with a defined replayable-state change). Every ledgered op with the agent as
  actor also updates the field, so checkpoints fill only the ephemeral gaps.
- **Restart grace, cumulatively bounded**: at boot, an agent gets grace to
  `boot + TTL` **only if its `last_coordination_at` is within one TTL**; otherwise
  the boot sweep immediately ledgers its `stale`/`dormant` transition: healed, if
  the agent is in fact alive, by its next call's `wake`. A crash-looping
  daemon cannot keep an abandoned agent active: the abandoned agent makes no calls,
  so `last_coordination_at` ages past TTL and the first boot after that transitions
  it. Incoming mail and other agents' activity are irrelevant by construction (§6).
  Grace is bounded by evidence, not by boot count.
- **Lifecycles**:
  - ephemeral: `active → stale` (lease lapse or process death; claims released;
    the awareness gate is NOT re-armed, §6) `→ archived` after 30 min grace (**token invalidated; the nonce
    is kept**, so the identity stays recoverable for `archive_retention`).
    `stale → active` only via ledgered `wake`.
  - persistent: `active → dormant` (lease lapse or process death: for a standing
    role, process exit is an expected end of activation; claims released, slots and
    mailbox retained; the awareness gate is NOT re-armed, §6) `→ archived` after `dormancy_max` (30 days from
    the ledgered `dormant_since` transition; token invalidated, nonce kept).
    `dormant → active` via ledgered `wake` (any authenticated call) or `resume`.
  - **`archived` is idle, not retired.** For `archive_retention` the row, its
    mailbox and its nonce remain, so mail may be SENT to it, a wake may be
    attempted for it, and it returns to `active` by `resume` or by registering
    again with the same name and nonce. Only `closed` (a deliberate `sign_off`)
    and a purged row refuse those. This distinction is what makes the wake
    promise hold: an ephemeral agent reaches `archived` in `agent_ttl` +
    `stale_grace`, five minutes plus thirty on the defaults, which is a length of
    quiet, not a decision.
- **Deadline diagnosis cascade**: expiry records `expired_unanswered` (recipient
  active), `expired_recipient_dormant` (persistent recipient asleep: visible in its
  inbox on wake, past deadline, within §8 retention bounds), or
  `expired_recipient_dead` (ephemeral recipient stale/gone). The dead/dormant detail strings state: *loss of coordination is not
  proof the recipient's work stopped; verify independently before touching its
  directories.*

## 8. Mailbox

Messages go agent → agent; identity = send serial; bodies private (§4, §5).

| Type | Expects | Dispositions (recipient) |
|---|---|---|
| `notify` | nothing | optional `ack` |
| `question` | an answer | `answer`, `decline` |
| `request` | a decision | `approve`, `deny`, `decline` |
| `handoff` | nothing | optional `ack` |

**Normative state machine** (every transition is a ledger event):

| From | To | Trigger | Event |
|---|---|---|---|
| (no message) | `pending` | `send` (with `op_id` dedup, §4) | `message.sent` |
| `pending` | `delivered` | recipient **retrieves the body** via `inbox` or `read_mail`, metadata polls (`events_since`/`await_events`) do NOT deliver | `message.delivered` (via ledgered `mark_delivered`, idempotent) |
| `pending/delivered` | `acked` (terminal + consumed for notify/handoff; non-terminal for question/request) | `ack` | `message.acked` |
| any terminal state | same state, `consumed` set | `ack` on terminal mail = consumption (§below) | `message.consumed` |
| any terminal state | same state, `outcome_read_serial` advanced | **sender** `read_mail`, fully quoted inline outcome prefix, or progress-event `ack` | none (ledgered `outcome_read`, idempotent) |
| retained approved/done request | same state, `review_read_serial` advanced | **recipient** `read_mail`, fully quoted inline review prefix, or review-event `ack` | none (independent ledgered read; never consumes the sender's outcome view) |
| `pending/delivered/acked` | `answered` / `approved` / `denied` / `declined` | `respond` (per type table) | `message.<state>` |
| `pending/delivered/acked` | `expired_unanswered` \| `expired_recipient_dormant` \| `expired_recipient_dead` | deadline sweep (§7 cascade) | `message.<state>` |
| `expired_unanswered` (question only) | `answered` | late `respond(answer)` by its recipient; expiry detail cleared and sender's verdict-read marker reset | `message.answered` |
| `pending/delivered` (notify only) | `displaced` | evicted by a newer notify at mailbox capacity | `message.displaced` (same serial as the displacing send, atomic) |
| `pending/delivered/acked/queued/approved` (request) | `withdrawn`, unconsumed recipient receipt | sender `respond(withdraw)`; already-performed approvals refused | `message.withdrawn` |

**Terminal predicate (exact, used consistently by capacity, displacement, inbox,
retention, and GC):**

```
Terminal(m) ⇔ m.state ∈ {answered, approved, queued, done, withdrawn, denied, declined,
                         expired_unanswered, expired_recipient_dormant,
                         expired_recipient_dead, displaced}
            ∨ (m.state = acked ∧ m.type ∈ {notify, handoff})
```

An expired-unanswered **question** may receive a late answer while retained.
Other terminal verdicts remain final, and expiry never reopens a request's
approval or grant. This narrow exception preserves answers historical live
writers accepted while a sleeping host's process-local monotonic clock paused:
the ledger recorded wall time, so cold replay otherwise expired the question and
refused its already-accepted answer. All new folds normalize the supplied `now`
to wall time before decisions or storage; no clock is read inside core.

For **expecting types** (question/request), `acked` is non-terminal: the message
still awaits a response. For **non-expecting types** (notify/handoff), `ack`
is the natural end of life: `acked` is terminal *and counts as consumed* (the ack is
the consumption, §below). `pending` and `delivered` are non-terminal for all types.
Capacity counts non-terminal messages; displacement targets notifies in
`pending`/`delivered`; `displaced` is a mailbox-eviction outcome, distinct from any
response outcome.

**Consumption is a flag, orthogonal to state**: `consumed` marks that the recipient
has acknowledged a message *after* having its body. It is set by the recipient's
ledgered `ack`: which is also **defined on already-terminal messages as
exactly this consumption transition** (state unchanged, `consumed` set), or by the
recipient's `respond` (responding proves receipt). GC eligibility requires
`Terminal(m) ∧ m.consumed`, or retention-cap eviction (watermark-recorded, §below).

**Reading:**
- `inbox()`: the recipient's non-terminal messages **plus unconsumed terminal
  messages** (bodies decrypted); marks pending → delivered.
- **`read_mail(msg_serial)`**: full message including body and response, authorized
  for **sender or recipient**. This is how a question's sender reads the answer
  (terminal events carry serials, never bodies). Recipient reads mark delivery.
- **Inline outcomes:** `check_in`, `inbox`, delivering lifecycle hooks and
  socket digests share a mail-first body budget of 1,600 Unicode characters,
  700 per body, and at most 16 outcome units. Unquoted selected units within a
  request collapse into one counted `read_mail` summary; that summary does not
  consume them or enlarge the unit allowance. Newest requests first, oldest
  unread prefix within a request. Complete units advance a durable read prefix;
  partial quotes and pointers do not. Socket writes alone never do; the existing
  confirmed new-turn receipt may read only the exact participant prefix quoted.
  Outcome and recipient-review reads are independent from envelope consumption.
  Event `ack` reads the acknowledged prefix, returning older unread event serials
  in `also_read`; repeating it appends nothing. `initialize_review_read` records
  the first upgraded serial once, treating older sender progress and recipient
  review units as read in the derived views without changing
  any historical op. Downgrading past this new op kind is not supported.
  Genuinely unread legacy verdicts and queue changes retain their existing
  durable read rules and still deliver after an upgrade.
- **Reading never consumes: acknowledgement consumes.** A crash between fsync and
  reply must not lose mail the caller never received, so no read (`inbox`,
  `read_mail`, `check_in`) ever commits consumption. Consumption happens only via
  the recipient's explicit ledgered `ack` or `respond` (see the consumed
  flag above): post-receipt by definition: the client sends it only after it has
  the body. Until consumed, the message keeps appearing in `inbox`/checkpoints
  (idempotent reads); once `Terminal ∧ consumed`, it is GC-eligible **after a
  recorded review-retention deadline, or the legacy 15-minute window when no
  deadline was recorded** (erratum E1, found by real-agent
  testing: without the window, GC raced the *sender's* `read_mail` of the
  response: respond marks consumed instantly, and the outcome vanished within
  a sweep tick). New responses record `retain_until` once in the originating
  op: normally 24 hours after that response. An unresolved review flag uses an
  explicit distant deadline until correction or acceptance; the terminal cap
  still applies. Legacy ops omit this field and replay their original GC
  decisions unchanged. Sweeps do not generate extra retention-update ops.
  Progress on done is allowed only while a review flag is unresolved, and
  appends a correction without changing the original done verdict, response,
  deliverable or owed-work status. Clearing a flag matches its exact milestone;
  a milestone-zero whole-work flag needs milestone-zero progress or accept.
- **Loss is observable, not just ledgered.** When retention caps force eviction of
  retained terminal mail (128 terminal/agent, oldest-first), the recipient's replayable
  **`truncated_before_serial`** watermark advances past the evicted serial and is
  returned by `inbox()` and `check_in()`. A recipient whose cursor precedes its
  watermark *knows* mail in that range may be gone: even after ring rollover or
  restart has erased the eviction events themselves. Within retention bounds,
  "seen on wake" is guaranteed; beyond them, loss is explicit and detectable.
- Every read returns the current `serial` as the caller's cursor.

**Backpressure**: mailbox cap counts non-terminal messages. At capacity a notify may
displace the oldest notify; if nothing is displaceable, sends fail `E_MAILBOX_FULL`.
Nothing expecting an answer is ever displaced.

**Deadlines**: default 10 min, max 2 h: except sends to `persistent` agents, where
`deadline_s` may extend to 7 days (dormancy-aware). Sending to a dormant agent succeeds
and returns a warning: pick a deadline matching expected wake latency, or use
`notify`/`handoff` (no deadline).

**Mappings** (v2 gateway / v1.x Tasks): A2A. `pending/delivered → submitted/working`,
`answered/approved → completed`, `denied/declined → rejected`, `expired_* → failed
(timeout)`. MCP Tasks: all Dibs outcomes map to `completed` with the outcome in the
result payload (`failed` is reserved by MCP for execution failure, and expiry/denial
are outcomes, not failures).

## 9. Directory claims: advisory, and honest about it

`claim(path, mode, note?)` / `release(path)`; TTL-leased, public.

| Requested \ Existing (another agent's) | none | `shared` | `exclusive` |
|---|---|---|---|
| `shared` | ✅ | ✅ | ❌ |
| `exclusive` | ✅ | ❌ | ❌ |

Refusals return the full overlap list; grants return co-existing overlaps. Re-claiming
your own path renews (`claim.renewed`, ledgered).

**Path identity**: absolute, cleaned, **component-wise** prefix matching (`/x/y`
covers `/x/y/z`, never `/x/y2`); best-effort `EvalSymlinks` at ingress. Caveats
documented, not solved: case-insensitive volumes, Unicode aliases.

**Two claims overlap under either of two rules, and the result says which.**

- `path`: their absolute paths overlap component-wise, as above, **and there is
  no positive evidence the two agents are on different computers**. One daemon
  serves agents on other machines (§16), so absolute paths from two filesystems
  arrive in one namespace and `/Users/kim/src/api` on two laptops is two
  unrelated trees. Evidence is a `host_id` on both sides that differs: the
  claimant's, against the host the claim was TAKEN on, which is recorded on
  the claim like its repository is (a holder that later reports another
  machine does not take its claims with it). An agent that supplied none
  collides exactly as it did before the field existed.
- `repo`: both agents are positively in ONE repository (shared Git common
  directory, equal configured remote, or equal root commits: the ranking of
  §9's `differentProjects`, read for sameness rather than difference) **and**
  their paths overlap once each agent's own checkout root is subtracted.

The second rule exists because one absolute path is not one file. Two linked
worktrees of a repository hold `/a/wt1/x.go` and `/a/wt2/x.go` for the same
tracked file, and the first rule alone reports nothing. The portable name is
recorded on the claim when it is taken, from the checkout root the server
resolved at registration, together with WHICH repository it is relative to;
a renewal restates all of it from where the holder is now. A path outside the
agent's own checkout has none, and the repository rule does not apply to it. Both halves demand positive evidence:
an overlap fired on an absence of evidence is a conflict between strangers, and
that is worse than the collision it would catch. The same rule is what will
carry claims between machines, where absolute paths stop meaning anything at
all: see `docs/NETWORK.md`.

**Lifecycle**: renewable 15-min lease, hard max 24 h. Claims end when their agent
leaves `active`: on `stale`, `dormant`, `closed`, and `archived` alike.

**Honesty rules (normative for all surfaces)**: claims are advisory; dibd cannot
prevent filesystem writes. Claim expiry/release means the *coordination signal*
ended: never that the holder's processes stopped or that writing is safe; verify
independently. `dibs audit` (v1.1) is a heuristic that cannot identify writers.
Read-only work needs no claim.

## 10. Attention: deliberate polling, with a complete cursor contract

- `events_since(since_serial)`, non-blocking catch-up.
- `await_events(since_serial, timeout_s ≤ 60)`, long-poll; parks server-side, wakes
  on the first matching event; the check-then-park is race-free under the serial
  cursor.
- Agents see: events addressed to them, their own agent's events, all public events.
  **Events carry metadata only** (serials, types, agent ids), bodies come from
  authenticated reads (§8), which is why metadata polls don't mark delivery.
- **Cursor recovery, one atomic checkpoint**: the event ring holds the most recent
  65,536 events and is empty after restart. A cursor older than the ring floor gets
  `E_CURSOR_TOO_OLD` with the recovery in its hint: call **`check_in()`**, which
  returns `{board, inbox, serial}` computed **at a single point in the loop**: a
  coherent serial cut (trivially atomic under the single writer; no interleaved
  change can fall between board and inbox), doubling as the awareness gate. The
  awareness acknowledgement **and** the delivery transitions of any returned
  pending mail are effects of this same `check_in` op, at its single serial; the
  returned snapshot is the **post-state** (returned messages already show
  `delivered`, and the returned serial is the op's own). Resume polling from *that*
  serial. Catch-up is **state-convergent within retention
  bounds** (§8): board and inbox are state, events are how you watch, and where
  retention has pruned history, the loss is explicit and bounded, never silent.
- **SSE (web UI)**: one frame per op: all of an op's events ship atomically in one
  SSE message with `id: <serial>`, so `Last-Event-ID` resume can never split an op.
  (This is Dibs' own UI stream, untouched by MCP 2026's removal of resumable SSE.)
- **A subscription that resumes past the ring is resynced from the inbox.** A
  `subscriptions/listen` carrying `com.dibs/since` older than the ring floor
  cannot have its gap replayed from the ring, and an empty replay would leave a
  question that arrived in the gap waking nobody. The notices the ring would
  have carried are rebuilt from the mail: a `message.sent` for each message
  still waiting that arrived after the cursor, a `message.adopted` for blocking
  mail moved in after it, and the verdict (`message.answered` and the rest) on
  each question or request this agent sent that was answered after the cursor.
- **A live subscription that dropped an event refills from the ring.** The
  channel behind a stream is bounded and the loop drops rather than stalls
  when it is full; the drop is recorded, and the stream replays from the ring
  everything after the last EVENT it delivered before continuing: one op emits
  several events at one serial, so the position is (serial, sub) and the
  position's own serial is re-read. Repeats coalesce at the subscriber, which
  dedupes by serial.
- **A subscription follows its agent only where its agent is.** A
  `subscriptions/listen` may name the harness session it serves
  (`_meta["com.dibs/session"]`, which the stdio bridge attaches: the thread
  the harness named, else the bridge's own session id); while the agent
  holds that session the inbox is delivered, and while the agent is in
  another one it is withheld, so a bridge left behind by an identity that
  moved cannot wake the session the agent left. A thread is held only while
  it is the agent's current session, because the row retains every thread
  it has been bound to. A stream that names no session, or follows the
  board alone, is not measured. Board notifications are
  unaffected, and the inbox returns with the agent. A stream ends with the
  token it was opened with: once that token is rotated away, whoever holds
  the new one subscribes afresh.
- Polling is a **product choice**: MCP 2026-07-28 offers `subscriptions/listen`;
  adopting it is a v1.x option that changes no semantics (the cursor model stays).

**Bridge-only wake surfaces.** `dibs://wake` is the host bridge's delegated
wake subscription, not an agent-facing resource. `dibs://wake-digest` is a
hidden `resources/read` for the in-session socket writer, requiring that
request's `_meta["com.dibs/token"]` and `_meta["com.dibs/session"]`. It returns
one private, immediately stale (`ttlMs: 0`) plain-text content item: the current
digest, or empty when nothing is owed or the agent no longer holds that session.
It neither marks mail delivered nor drains agent updates or announcements;
unlike `inbox` and `hook_poll`, it spends no delivery or agent call budget.
Credential and session checks share the digest's single writer-loop snapshot.

Socket digests require an actionable cause and idle or recovered unknown lifecycle.
Starting and tool hooks and every authenticated model/tool call establish or
refresh busy; observer subscriptions do not. A token can also be used by a CLI,
subagent or plugin outside the session's turn, and a finishing hook can be lost.
After 30 minutes without a busy observation, busy becomes unknown, never idle,
so missing Stop evidence cannot permanently disable waking. A finishing hook
establishes idle immediately. Unknown lifecycle retains the existing bounded
contact/boot grace, then permits one coalesced actionable or due-wait wake.
Every message written by an agent or human to the recipient independently
qualifies for delivery, including a plain notify that asks for no reply.
Dibs-generated progress, accepted reviews and queue updates do not independently cause
socket delivery. Answers,
denials, declines, flagged reviews and grant/adoption verdicts do. Approval of
an ordinary work request is informational: it accepts work without performing
a permission or mailbox effect. Eligibility reads the request's typed `grant`
and `adopt` fields, never the notice text. DONE qualifies only when the sender currently
holds a declaration with `waiting` set. Full outstanding mail remains available
to delivering hooks and authenticated pulls.

Due announcements awaiting this agent's required acknowledgment also qualify,
under the existing presentation cadence; acknowledgment quiets both routes.
Stop and SubagentStop use this same typed actionable cause before blocking a
finished turn. Informational progress, ordinary approvals, accepted reviews,
queue acceptance/position changes do not independently block. An authored
notify blocks once and delivers its words; a repeated Stop does not deliver it again.
Leaving an already-presented notify unacknowledged does not rearm it across
later idle epochs, command reconsideration or app reconnect. A confirmed
socket presentation and ledgered mailbox delivery count; an unconfirmed
socket write and command execution alone do not count as read receipts.
A non-blocking Stop neither marks those items delivered nor reads their outcome
prefixes. Held information is delivered through SessionStart, `check_in` or
`inbox`, or included in the next actionable Stop/socket digest under the shared
quote budget. UserPromptSubmit remains silent. Due declared waits and bounded
declared-work continuation remain independent Stop causes.

A send result's live route note uses the same authored-message decision as
socket and Stop delivery. Idle authored notify receives a best-effort wake
attempt; authored mail to a busy session reports delivery deferred until its
Stop hook. That Stop blocks and delivers the mail without a socket frame.
Socket availability and kernel writes cannot confirm receiver acceptance.
Later mail in an already-written idle epoch reports coalescing without claiming
that another frame was sent; it remains available to the next delivery.

The additive socket-offer handshake reserves one derived wake epoch per host
identity and current session. A successful kernel write holds that epoch until
actual turn evidence; it proves no receiver acceptance. Failed writes release
the reservation. Starting hooks confirm presentation of every owned mailbox
quoted in the accepted batch, without consuming raw mail. A bridge may supply
additional mailbox tokens in `com.dibs/socket_tokens` only when the daemon
advertises `com.dibs/socket_batch`; every token is authenticated separately and
other host/session rows are excluded. Old dormant bridges can deliver one
pre-upgrade scheduled notice and may quote only one mailbox under the new
shared reservation; full hook/pull fallback remains available.

Due declared waits use per-slot clocks and quote only the slots due now. A
derived `socket.ready` hint on the existing subscription has no ledger serial
or ring cursor. Busy suppression spends no retry; hook delivery advances the
cadence without spending a native-write retry. Native due rechecks retain the
three-write bound and stalled-row/assigner reporting; open-work backoff remains
10/30/60 minutes. Daemon socket failures retain one retry for the current
actionable cohort, while a changed cause or real turn rearms it. All lifecycle,
reservation and presentation records are derived; losing them can repeat a
notice but cannot lose coordination state.

**App reconnect recovery.** Local stdio bridges attach their own PID and process
start stamp as additive per-request metadata, including modern discovery and
legacy startup. On macOS the daemon independently observes the owning ChatGPT app's PID
and start time, once per bridge incarnation in a bounded cache. A previously
unseen app incarnation triggers one normal delivery reconsideration for existing
app-owned rows on that host with pending requests, questions, handoffs or notices.
No identity, activity, session binding or ledger state changes. Inferred queued
receipts are invalidated by app generation, independent of wall-clock ordering;
an authoritative pending app notice renews that generation without duplication.
Mail is rechecked before delivery; ordinary cooldown, loaded-thread and away
opening policy still apply. Empty mailboxes and generated informational updates alone do
not cause reconnect wakes. Lost derived caches permit one bounded recheck.
Sender notes distinguish observed queue acceptance from an unconfirmed attempt.

All three delivery routes share the configured phase: default `all` admits
authored notify; explicit `urgent` suppresses FYIs while admitting blocking
mail, and `none` suppresses mail wakes. Generated updates never qualify solely
because the phase is `all`. Suppressed mail remains available to natural
activations and authenticated pulls.

**Native queue admission.** A pending Dibs notice of any canonical mail-event
kind coalesces later notices. The native runner serializes observation, enqueue
and retained receipt with a private per-thread OS file lock shared by daemon
and bridge processes using that board directory. Queue fallbacks use the same
door. Failed admission does not enqueue or report acceptance. Contention timeout
is logged at Debug, skips fallback execution and leaves the engine to retry.
Real lock errors retain their warning and ordinary fallback qualification. Older running
writers do not participate until upgraded. When observation is unavailable,
the retained fallback may add at most one duplicate per current prompt, new
reconnect generation or two-hour receipt expiry; this preserves lost-wake
recovery. An observed empty queue re-arms immediately, and an observed pending
notice wins over those inferred causes. App opening keeps its existing away
policy. No queue item is deleted and no coordination state is changed.

A newer self-wake notification also advertises `com.dibs/socket_offer: true`.
A capable writer includes that key on the hidden digest read to reserve the
current presentation, receives `_meta["com.dibs/socket_offer_id"]`, and reports
that id with `com.dibs/socket_written` after its attempt. A successful write is
not a harness receipt: subsequent actual activity confirms presentation only
if that session was idle when offered, or a starting lifecycle event identifies
a new turn. Tool calls in an already-running turn cannot confirm held mail.
A held message or a failed write therefore retains its Stop fallback. Stop
presentation filters a later socket refresh too. This timing is ephemeral,
changes no ledger/mailbox state, and preserves existing reminder intervals.
Plain reads and older writers remain non-consuming; advertising a capability
does not spend anything before the writer implements its handshake.
Agent updates share the presentation cadence. Informational updates actually
included in a delivering Stop are not presented by later hooks again, even
after the cadence expires; blocking notices retain their reminder rules.
`check_in` and `inbox` still expose the complete pending information.
Accepting or flagging a milestone clears the progress notice for that request,
like reading its full message. `ack` also accepts a retained progress/review
event serial, dismissing only that event for its intended current-incarnation
recipient. This derived acknowledgment does not review or consume the parent,
advance the coordination serial, or write a new ledger op. Strangers and reused
identities cannot resolve the parent through an event. `respond` on one's own
event returns a hint naming the parent and review call. `read_mail` derives
`milestone_reviews` (unreviewed/accepted/flagged, by and at) from ordered entries;
a newer worker report supersedes an earlier review of that milestone.
Accepting a numbered step requires its worker report. A DONE request itself
reports its final named milestone, including when progress carried no index;
it does not fabricate reports for unreported intermediate steps. This admission
rule does not change historical replay, numbered-progress counts, the original
DONE verdict, or its deliverable.

The session id is a same-machine capability for a token-less nudge. A local
peer holding the board secret and knowing that id can call `hook_poll`, or
forge a starting-hook event, to spend presentation for an `AnnounceRetry`
interval, or suppress later hook presentations of informational notices already
carried by Stop; authenticated pulls still retain them. Repeating a forged
event can keep blocking reminders delayed. This accepted trade
does not consume or hide information: the pending mail and agent updates
remain complete in `inbox` and the agent's own authoritative `check_in`.

A self-wake inbox notification advertises this read with the additive
`com.dibs/digest_refresh: true` metadata key. A capable bridge refreshes before
each socket attempt, including deferred, retry and upgrade-handoff deliveries;
the daemon refreshes socket, command and delegated plans too. Empty means no notice is written,
no cooldown is spent, and an obsolete retry is cleared. A failed read never
falls back to captured text. Coalesced mailboxes share one socket writer and
one fresh notice. Older daemons and dormant pre-upgrade bridges retain the
previous behavior until the bridge upgrades between stdio requests; the new
key does not change the one-writer declaration or add another wake route.
Pending and delivered mail still requires `ack` or `respond`; `read_mail` alone
does not silence it. Announcement retries use the existing hook cadence: an
unacknowledged announcement not yet due does not justify a placeholder wake.

## 11. Limits (all enforced; defaults, human-tunable)

| Resource | Default | On exceed |
|---|---|---|
| ops per agent | 10/s, burst 30 | `E_RATE_LIMITED` (no wake, no ledger) |
| live agents / persistent agents | 64 / 64 (the persistent ceiling follows `max_agents` when only that is set lower) | `E_AGENT_LIMIT` |
| slots per agent | 32 | `E_SLOT_LIMIT` |
| claims per agent / global | 32 / 256 | `E_CLAIM_LIMIT` |
| mailbox depth (non-terminal) | 256 | §8 backpressure |
| terminal messages retained | 128 per agent, then GC'd (ledger keeps history) | pruned oldest-first |
| archived agents retained in state | 7 days, then GC'd (with nonces + dedup records); addressable and wakeable throughout | pruned |
| message/slot body | 32 KiB | `E_TOO_LARGE` |
| name / description / note / path | 128 B / 1 KiB / 512 B / 1 KiB | `E_TOO_LARGE` |
| dirs per slot | 16 | `E_TOO_LARGE` |
| nonce / op_id / resume_id | 64 B–128 B | `E_TOO_LARGE` / `E_BAD_NONCE` |
| dedup records | 24 h **or** the 256 most-recent identified ops, whichever is reached first (§4) | pruned oldest-first |
| resume rate | 1 per 10 s per agent | `E_RATE_LIMITED` |
| agent lease TTL | 5 min | → `stale`/`dormant` |
| stale grace / dormancy max | 30 min / 30 days (from the ledgered transition, §7) | archived |
| claim lease / hard max | 15 min / 24 h | `claim.expired` |
| deadline | 10 min default; 2 h max (7 d to persistent agents) | §7 cascade |
| await_events timeout | 60 s | returns empty |
| event ring | 65,536 | `E_CURSOR_TOO_OLD` → §10 checkpoint |

A rejected domain op is never ledgered and receives no serial of its own: though a
phase-4 rejection may legitimately have caused a *preceding* `wake` or
`activity_checkpoint` serial (§2 wake phases: the admitted attempt is real, even
when its operation fails).

## 12. MCP surface. 2026-07-28, dual-version

**Primary contract: MCP 2026-07-28 (stateless; SEP-2575).** As of 2026-07-22 this
revision is a **release candidate** (RC locked 2026-05-21; final publishes
2026-07-28). Dibs builds against the RC and treats the dual-version path (below)
as load-bearing until the final ships and hosts migrate.
- `server/discover` implemented: returns `supportedVersions`, capabilities,
  `serverInfo`, and the five-sentence protocol `instructions`.
- Per-request `_meta` validated: `io.modelcontextprotocol/protocolVersion` (must match
  the `MCP-Protocol-Version` header, else 400), `clientInfo`, `clientCapabilities`.
  Unsupported versions → `-32022` with the supported list.
- No `initialize`, no `ping` on the 2026 path. `subscriptions/listen`: v1.x option
  (§10); v1 polls.
- **Legacy path (SEP-sanctioned dual-version)**: `initialize`/`notifications/
  initialized`/`ping` retained for 2025-11-25 hosts: today's clients work day one,
  and the legacy path sunsets when hosts migrate.

**Tracked requests (2026-07-28 tasks extension).** `send(type: "request",
track: true)` keeps the request for seven days from creation unless its
recipient is purged first. Shortening archive retention (default seven days)
can purge that recipient sooner; a board reset also invalidates the handle.
If that call declares `io.modelcontextprotocol/tasks` in its per-request
client capabilities, it returns `CreateTaskResult` with `resultType: "task"`;
otherwise it returns the ordinary send result with an explicit `tracking`
note explaining that there is no task handle and to follow with `read_mail`.
Legacy calls always keep the legacy result shape, even when they carry modern
capability metadata; task methods on that era return `-32601` with a hint to
use the modern protocol. Tracking is opt-in because
some hosts wait for a task to finish before returning control to the agent.
The handle is a bearer capability authenticated by the board's secret and a
128-bit MAC: possession grants access to that task, not to other mailbox
items. Task requests also pass the daemon's normal coordination auth gate.
Handles survive daemon restarts with the same secret and node identity.

`tasks/get`, `tasks/update`, `tasks/cancel`, and listeners requesting
`notifications.taskIds` require the extension capability on each call;
absence returns `-32021` with `requiredCapabilities.extensions` naming it.
Unknown handles return `-32602`. Requests remain `working` through acceptance
and milestone reports, with progress in `statusMessage`. Done returns
`completed` with the deliverable; denial, decline and expiry also complete,
with a tool result carrying `isError: true`, rather than a protocol failure.
Task listeners send current snapshots as `notifications/tasks`, recover
dropped events from those snapshots, and end when their followed tasks end.
Combined resource/task listeners honor the resource subscriptions only and
omit task IDs from their acknowledgement. No task progress notifications or
client input requests are emitted. `tasks/update` ignores unsolicited input;
`tasks/cancel` acknowledges cooperative cancellation without stopping another
agent's work or granting sender authority. Sender `respond(withdraw)` retracts
an unfinished request and maps its tracked task to `cancelled`, with a factual
result and optional replacement reference. Task listeners publish this terminal
snapshot and stop following it. Neither operation stops an agent process.

**Tools (52).** All take `token` except `register`, `resume`,
`hook_poll` and `guard_path` (the last two are lifecycle-hook surfaces and have
no token to give: see SECURITY.md).

The table below is the v1.0 core, and is kept because §12 is the frozen contract
those tools were reviewed against. It is NOT the full surface: v1.1 added blobs
(`put_blob`, `get_blob`; now `upload`, `download` for the out-of-band byte plane
specified in SPEC-ATTACHMENTS.md A13), the human/hook surfaces (`hook_poll`, `guard_path`,
`bind_session`, `broadcast`, `all_mail`, `board`), and v1.2 added the
space surface (`open_space`, `join_space`, `read_space`, `post`,
`announce`, `ack_announcement`, `leave_space`, `watch_space`, `admit`,
`evict`, `merge_spaces`, `lock_space`, `unlock_space`) and
`vouch_child`, specified in SPEC-CHANNELS.md, plus `force_release`, the
claim-level counterpart to `unlock_space`. v1.3 added `configure` and `merge_agents`, the
admin-gated read and write of the settings the engine can apply while it
runs; see docs/CONFIGURATION.md for why an address is not among them.
Every board row carries `work` (`idle`, `working`, `declared` when the agent
has not been seen in 30 minutes, `waiting`, `stalled`),
derived by the daemon from the agent's declarations and the turns it has seen,
never from process liveness, and outside the ledger like every other liveness
view. See WAKE-MECHANISMS.md, "Continuing a turn that ends with declared work".
v1.4 added `relocate`: running a closed agent's thread in a different
environment from the one it last ran in, which a wake never does. It needs the
coordinator or admin role, or the `relocate` permission the human grants
(`dibs admin may-relocate <agent>`, or by approving a request carrying
`grant: "relocate"`). Every relocation is ledgered as `agent.relocated` with
who, the agent, and from and to; the board row shows the last one.

The sender can retract an unfinished request with `respond(disposition:"withdraw",
body: reason?, superseded_by: serial?)`. Engine ingress translates this into the
new `withdraw_message` ledger op before recipient response guards. Admission
checks field shapes and rejects work-report fields; the fold checks sender
ownership, creation-serial privacy fence, request state and replacement. Only
pending, delivered, acknowledged, queued and approved requests qualify; an approved grant or
adoption already performed its effect and cannot be withdrawn. Questions expire
and are not withdrawable. Unknown, other-sender or self replacement references
are refused; a replacement is another ordinary request by the same sender,
possibly to a different recipient, and this operation never starts it.
Withdrawal retains approval/progress history in separate fields, clears queue
debt/rank/task ordering lock, compacts remaining ranks and notifies the recipient
without an imperative. The terminal envelope becomes unconsumed until recipient
`ack`, and its receipt is rebuilt from that state after restart. Sender receipt
is the successful call; no delivery is asserted. No work is owed on a withdrawn
request, and no declaration or agent process is altered. Historical ops retain
their exact semantics; old field names remain unchanged.

Ordinary work requests can be accepted with `respond(disposition:"queue")`.
Queued is an approval verdict, but not started work. New acceptance records an
explicit `queue_debt` marker: queued and approved debt remains owed until done,
declined or withdrawn, within the mailbox capacity bound. Older unmarked approvals retain
the historical 24-hour obligation window; replay never infers the new marker.
`approve` starts queued work; completing it never starts another request.

The queue defaults to descending priority (urgent, high, normal, low), then
response deadline and arrival serial. New arrivals preserve existing relative
order. `queue_update` is recipient-owned; it records priority overrides,
restoration of sender priority, and before/tail ordering. `queue_lock` authenticates
coordinators/admins inside the writer loop and records their identity for the
existing `queue_order_lock` permission, scoped to an agent or queued request.
The existing human HTTP admin gate also grants/revokes it. Locks protect relative
order, including indirect crossing, and never prevent starting or removing work.
Public queue projections contain metadata only; mailbox readers see bodies.
The ordered queue at approve/queue/done, check_in and inbox is the reminder:
`overdue` and `overdue_s` report whether and how long the response deadline is
past. There is no separate queued-debt hook reminder or queued-only wake.

`tools/list` is the authority, it serves `toolDefs` verbatim, so the served
surface and the advertised one cannot drift. Ask a running daemon rather than
counting a document; this line said 17 for two minor versions.

| Tool | Purpose |
|---|---|
| `register(name, description?, pid?, nonce?, kind?)` | → `{agent_id, token, serial, board, nonce?}`; a nonce is expected for `kind: persistent` and MINTED when omitted, never refused (§4) |
| `resume(nonce, resume_id, pid?)` | reactivate a persistent agent: rotates token, bumps activation generation, rebinds PID, wakes, re-arms gate; idempotent per resume_id (§5) |
| `check_in()` | pass the awareness gate (per credential: a new session must look again); → atomic `{board, inbox, serial}` checkpoint (§10) |
| `update(name?, description?, title?, branch?, model?, provider?, effort?, surface?)` | revise what the agent says about ITSELF. The id is immutable (it is the address every message, claim and membership keys on), so a rename moves the label only, and a name another live agent holds is refused (`E_NAME_TAKEN`) rather than suffixed. `harness`/`version` are not settable: the client states them at the handshake, which is the only part of an identity that is not self-reported. Empty `description` clears, because already-ledgered `update` ops did that; the fields added later merge when non-empty, so replay of old ops is unchanged |
| `sign_off()` | lifecycle |
| `heartbeat()` | renew lease while idle (implicit on every call) |
| `invite(action?, name?, ttl_s?, issued_by?, export?)` | private local issuers mint scoped cloud credentials and configuration; own-prefix children by default, four live/7d; list/revoke own invitations, no invited grandchildren. Only `export: true` on mint returns the private stable recovery nonce; ordinary/false mints omit it, and export on list/revoke is refused. This avoids unnecessary recovery-credential disclosure in ordinary agent transcripts. Direct-IP mode returns guest CA PEM/pin and explicitly no verified native-client configuration yet. Public listener and issuer-generation boundaries: docs/NETWORK.md §9 |
| `declare(slot_id?, text, dirs?, refs?, activity?, holds?, waiting?, recheck_after?)` / `undeclare(slot_id)` | declare/end work units. A declaration without `waiting` says the agent is working: a turn a Dibs wake started that ends while one is open is continued at Stop (at most twice per version of it, never after a person's prompt) |
| `send(to, type, body, deadline_s?, op_id?)` | → `msg_serial`; `op_id` = durable dedup (§4) |
| `respond(msg_serial, disposition, body?)` | answer/approve/deny/decline; `done` on a request you approved, once the work is delivered. An approved request not yet done is an obligation (the row's `owes`, for a day after approval) and counts as declared work |
| `ack(msg_serial)` | explicit read receipt |
| `inbox()` | non-terminal + unconsumed terminal mail, decrypted; marks delivery; returns `truncated_before_serial` (§8) |
| `read_mail(msg_serial)` | full message + response; sender or recipient (§8) |
| `claim(path, mode, note?)` / `release(path)` | §9 |
| `events_since(since_serial)` / `await_events(since_serial, timeout_s?)` | §10 |

**`waiting`**: every authenticated result from a mutating call carries a `waiting`
string when the caller has unread mail, an unacknowledged announcement, or a
pending agent update. Counts and the corrective call (`inbox`) only, never
content: the body stays behind the authenticated mailbox. Absent when there is
nothing, and absent on `check_in`, which has just returned the inbox itself.

This is a delivery guarantee, not a convenience. Push delivery through lifecycle
hooks is conditional on the harness having hooks, the plugin being installed and
loaded before the session began, and the agent having registered with the
session id the hook quotes; a tool result is the only channel that exists
unconditionally, and it cannot be misrouted, because it returns down the
connection the caller authenticated on.

**Errors**: structured `{code, message, hint}` tool results (`isError: true`); `hint`
names the corrective action. Codes: the §11 set plus `E_BAD_TOKEN, E_MUST_ACK_BOARD,
E_NO_AGENT, E_NO_SPACE, E_NO_SLOT, E_NO_MESSAGE, E_NO_CLAIM, E_MSG_FINAL, E_BAD_TYPE, E_BAD_MODE,
E_BAD_DISPOSITION, E_BAD_NONCE, E_NAME_TAKEN, E_NONCE_IN_USE, E_OP_ID_CONFLICT,
E_AGENT_CLOSED, E_CURSOR_TOO_OLD, E_NOT_A_MESSAGE, E_NOT_YOUR_MESSAGE`. The last two
are the honesty rule applied to `read_mail`: a serial that exists is never
reported as absent. An announcement's serial names the space that reads it, and
another pair's message names the pair (ids only, never the body). An inherited
serial, one below the caller's own creation, keeps the watermark wording, since
naming the parties there would tell a replacement that its name had a
predecessor.

**Resources**: `dibs://board`. Mailboxes are deliberately not resources.

## 13. Human window

`send(to: "human")` resolves the role at authenticated, rate-admitted ingress.
Its first use creates the person's persistent mailbox through the same
registration path as a web action, with this board machine's OS-owned name and
nonce and no process identity from the sender. Registration and the concrete
recipient are ledgered, so replay preserves the row and addressed message.
Observing the board and `HumanIdentity` still create nothing.
Mailbox creation remains ledgered if later send-domain execution refuses the
message; it is not rolled back with that refusal.
Requests and questions use the existing native notification or attached human
relay route;
the send result includes `human_route` (`relay`, `desktop`, `none`) and
`human_relay_count` (relays whose bounded queue accepted the notice). Full or
removed relays do not count; no accepted relay falls back to the local desktop
or `none`, with a corrective hint. This never waits for a person or blocks send.
`read_mail` includes derived `human_delivery`: route, aggregate state, and each
source's last receipt/error and retained posting/dismissal evidence. `posted` means
the OS accepted posting, not that a
banner appeared or the person saw it; `dismissed` requires explicit dismissal
or defer evidence, not a timeout. Queued/pending supplies no posting proof.
Posting/dismissal on one source outranks failure on another; a real human
response gives `answered`. Receipts grant nothing and use the relay's existing
authenticated session. No receipt state is ledgered: after restart the route
and state are `unknown` until new evidence arrives; actual answers replay.
Old helpers/relays supply no invented receipt. Retrying `op_id` returns the
original route (or unknown after restart) without another alert.
Local desktop sends additionally return this derived delivery evidence immediately:
`posted: false` while pending, becoming true only on OS posting evidence. Active
macOS Focus supplies its name, `shown: "unknown"` and a hint to check receipts
and leave the request pending. The posting receipt retains its own Focus snapshot,
even if Focus changes afterwards. These observations run outside the writer and
read only bounded non-secure Focus files; no app/contact filter is inspected and
no visibility claim is inferred from it. Human questions and requests never
open a decision window automatically, including while Focus is on. A person
pressing an answer button can still request an answer field or choice list.
Agents needing the person's decision send a request here rather than waiting
for a chat they may not read.

`dibs board` · `dibs messages` · `dibs log [--follow]` · `dibs verify [path]`
(keyless) · `dibs mcp-config` (prints host config incl. local-secret header) ·
`dibs version`. Web board at `/`: server-rendered, SSE-live (per-op frames, §10),
htmx plus one small authored script (`internal/assets/board.js`, ~320 lines:
composer state across redraws, relative timestamps, the admin dialog). It was
"zero authored JS" through v1.0 and the claim outlived the code; there is no
framework, no build step and no bundle, which is the property that actually
matters. Local-secret cookie. All human surfaces present §7/§9
honesty language verbatim.

## 14. Standards posture

Standards win wherever they cover a concept; Dibs invents only the uncovered core
(serial model, awareness gate, advisory claims, liveness honesty, bounded-state
regime). MCP 2026-07-28 is the agent-facing contract (§12). A2A v1.0 supplies message
lifecycle vocabulary (§8) and the v2 gateway data model (agent ⇢ AgentCard). **The A2A
gateway is v2 and is a separate listener** with its own TLS/auth/SSRF boundaries,
dibd's loopback bind is load-bearing for §5.

## 15. Multi-node (v2 design; v1 obligations only)

Federation of sovereign single-writers: ownership partitioning, never consensus;
causal cross-node order via `(node_id, serial)`; global cross-node serial rejected on
principle. Peering is a human act (pairing token, mTLS pinned to node keys). Remote
agents of a dropped peer show `unreachable`: never permission to proceed. Claims and
liveness never federate. **v1 obligations (implemented)**: stable `node_id`, `n` field
on ledger lines, `unreachable` in the status enum.

## 16. Transports

Local (v1): MCP streamable HTTP over loopback TCP. QUIC rejected on loopback merits.
UI (v1): SSE down + POST up; WebSocket/WebTransport rejected for this traffic shape;
SSE inherits HTTP/3 transparently if the stack beneath changes.

**An agent's machine is recorded.** A bridge asserts it
(`_meta["com.dibs/host"]`) on every call, and the assertion is taken on any
transport, because the documented transport for a machine without Supgang is
an ssh forward, which reaches the daemon over loopback like a local caller.
A loopback caller that asserts nothing is stamped with the daemon's own node
id, since nothing else can reach loopback. An assertion is exactly as strong as
the bearer credential that let it in and no stronger: §9's `host` rule is a
CORRECTNESS boundary, not an authorisation one, and it becomes the latter only
when a host can prove itself. See `docs/NETWORK.md`.

**An agent's app is recorded the same way.** The bridge reads it from its own
process ancestry and sends it as `_meta["com.dibs/surface"]`; it wins over the
`surface` an agent states, and a stated surface a wake acts on (`chatgpt-app`)
is ignored. A wake for an agent in the ChatGPT app opens its thread there when
the app is not holding it, and for no other agent (WAKE-MECHANISMS.md).

**Remote agents (v1).** One daemon serves agents on other machines directly: there is
no sharding, no replication, and therefore no split-brain: a single writer keeps every
guarantee (notably exclusive claims) trivially true. Bind a reachable address with
`--addr`; remote agents present the same access secret as a bearer credential
(`X-Dibs-Local` or `Authorization: Bearer`), which is already the auth model.

**Dibs secures itself: no flags, no third-party dependency.** The transport is chosen
for the operator, not by them:

- **loopback** (default) → plaintext; nothing else can reach it, so certificates would be
  ceremony.
- **any reachable address** → **HTTPS**, with a certificate generated into the data dir on
  first run. Serving a remote address in cleartext is never the default.

Overrides live in `<dir>/dibs.toml` (`addr`, `tls_cert`, `tls_key`, `insecure_plaintext`)
for operators who want their own CA or a fronting proxy. Dibs never requires a VPN,
an overlay, or an external CA to be safe out of the box.

Mesh (v2, multi-writer): QUIC/HTTP3, stream-per-concern, 0-RTT (idempotent exchange),
pinned-key TLS 1.3: deferred because merging hash-chained ledgers across writers needs
consensus or CRDT conflict resolution, and a single daemon makes it unnecessary for now.

## 17. Engineering standards

Pinned single stable toolchain (Go 1.26.5 via mise; 1.27 at final release, never RCs).
Pure core + command sourcing ⇒ deterministic simulation: the randomized
replay-equivalence suite (`state == fold(ledger)` under seeded op/time sequences) is
the load-bearing gate: it caught the v0.1 receipt and heartbeat divergence bugs.
golangci-lint v2 zero-warnings (cyclop ≤15, gocognit ≤20, funlen ≤512, file ≤2000,
gofumpt, gosec+govulncheck, nolintlint, forbidigo bans test sleeps); `-race` always;
coverage ≥85% on core+ledger; synctest for time logic; fuzz + kill-9 harness (v1.1).
Task + GoReleaser (reproducible, cosign, SBOM), lefthook, GitHub Actions. Two static
binaries (`dibd` and `dibs`) both CGO_ENABLED=0 and byte-reproducible.

## 18. v1 scope freeze

**In v1**: ledger (hash chain, encryption, torn-tail, fail-stop, GC); state tiers +
ledgered wake transitions; ephemeral + persistent agents; resume; awareness gate
per activation; mailbox (full state machine, read_mail, op_id dedup,
dormant-recipient semantics); claims (§9 matrix); bounded liveness with bounded
restart grace; limits incl. state GC; MCP 2026-07-28 dual-version surface (52 tools);
local access secret + Origin validation; CLI (board/messages/log/verify/mcp-config);
SSE web board; static binaries (`dibd` + `dibs`, no cgo, no runtime deps).

**v1.1**: rotation + snapshots; kqueue/pidfd exit notification; `dibs limits`;
`dibs audit`; fuzz + crash harnesses. (Wake-on-mail shipped in v0.0.7 as
`[wake.exec]` and the session-socket route, and `subscriptions/listen` is served.) **v2**: federation; A2A gateway (separate
listener); Ed25519 signatures; hub mode.

Anything not listed is out of scope for v1; additions require a spec revision first.
