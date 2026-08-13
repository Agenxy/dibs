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
2. **Engine-ephemeral state**: lease freshness touches (from reads/heartbeats), rate
   buckets, parked long-polls, the event ring. Never replayed, never trusted across
   restart. Ephemeral facts influence replayable state only by being **recorded as
   decisions inside ledgered ops** (sweep's `stale_agents`/`dead_agents`, wake ops).
3. **Presentation annotations**: `last_seen` (freshest activity incl. reads) and
   `proc_alive`. These appear in board/CLI/web *views*, computed live by the engine at
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
sweep` (only when it changed state), `mark_delivered`.

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
  recycled PID would silently reattach a fresh agent onto a dead agent's agent
  and its mail.

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
- **Registration nonce**: client-generated, ≥128-bit CSPRNG, **required for
  `kind: persistent`**, optional for ephemeral. Constant-time comparison. Two roles:
  - *Response-loss retry*: `register` with a nonce it has seen, while the agent is
    active and was created within one agent TTL, returns the original result
    (`resumed: true`). Outside that window: `E_NONCE_IN_USE` with hint → `resume`.
  - *Recovery credential* for persistent agents via `resume`. **Treat a
    persistent agent's nonce as a secret equal to its token.**
- **`resume(nonce, resume_id, pid?)`**: the explicit activation op for standing
  roles. `resume_id` (client-generated per attempt, ≥64-bit) makes it a **complete
  activation boundary**:
  - Verifies the nonce (constant-time); fails on closed/archived
    (`E_AGENT_CLOSED`/`E_NO_AGENT`) or unknown nonce (`E_BAD_NONCE`).
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
proc_alive`. Public; writable only by token holder. `agent_id` = uniquified name
slug. `activation` is a generation counter incremented by each `resume` (§5).
`last_coordination_at` is the agent's **latest durable coordination checkpoint**: a
conservative lower bound on its own last accepted authenticated call (it may trail
the true latest by up to TTL/2, the §7 coalescing interval). Updated only by
**ledgered ops in which this agent is the actor** (its own calls, register, resume,
wake; §7's `activity_checkpoint` refreshes it during ephemeral-only activity). Mail
*received*, probes, and other agents' ops never touch it: another agent cannot keep
an abandoned agent looking active.

**Kinds:**

- `ephemeral` (default), session-scoped. Status: `active | stale | closed | archived`
  (+ `unreachable` reserved for v2).
- `persistent`, a **standing role** (reviewer, nightly maintainer) whose agent idles
  between activations. Status: `active | dormant | closed | archived`. Dormant is
  deliberately not "stale": it is *expected* sleep. The agent, description, slots, and
  **mailbox stay live through dormancy**: mail queues while the agent sleeps; the
  serial cursor + §10 checkpoint give retention-bounded catch-up on wake (§8: within
  bounds guaranteed, beyond them explicit and detectable). Claims still
  expire on their own leases (§9). Registration requires a nonce (§5); reactivation
  is `resume`.

**Awareness gate**: before `declare` or `claim`, an agent must have called
`check_in()` **in its current activation**: the gate re-arms on every dormant/stale
transition and on `resume` (§2). Pre-ack writes fail `E_MUST_ACK_BOARD` (hint
names the fix). An agent that slept for a month cannot mutate the board on month-old
awareness.

**v1 is store-and-catch-up, not wake-on-mail**: mail to a dormant agent waits for the
agent's next activation (its harness, a schedule, or a human). `dibs watch --exec`
(v1.1) supplies the supervisor glue that turns queued mail into launched agents.

## 7. Liveness: three signals, honestly labeled

| Signal | Mechanism | PROVES | Does NOT prove |
|---|---|---|---|
| `dead` | `kill(pid,0)` per sweep + (pid, start-time) identity | The registered process is gone. Caveat: an unreaped zombie still *appears alive* to `kill(0)`; true zombie detection arrives with kqueue `NOTE_EXIT`/pidfd (v1.1) | That its children or in-flight effects stopped |
| `stale`/`dormant` | Lease lapse: no authenticated call for `agent_ttl` (default 5 min) if the agent gave a PID, or `idle_ttl` (default 45 min) if it did not, silence is weaker evidence than a dead process, and a token-only HTTP client never gives one | The agent stopped *coordinating* | That it stopped *working*, `stale + proc:alive` renders as "hung?", a hint, never a verdict |
| `expired_unanswered` | Deadline passed, recipient active | This message wasn't answered | Anything about recipient health |

- **Implicit heartbeat**: every authenticated call (reads included) refreshes the
  ephemeral lease. Explicit `heartbeat` is for otherwise-idle agents; ledgered only
  when it wakes/recovers an agent.
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
  - ephemeral: `active → stale` (lease lapse or process death; claims released,
    gate re-armed) `→ archived` after 30 min grace (token + nonce invalidated).
    `stale → active` only via ledgered `wake`.
  - persistent: `active → dormant` (lease lapse or process death: for a standing
    role, process exit is an expected end of activation; claims released, slots and
    mailbox retained, gate re-armed) `→ archived` after `dormancy_max` (30 days from
    the ledgered `dormant_since` transition). `dormant → active` via ledgered
    `wake` (any authenticated call) or `resume`.
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
| `pending/delivered/acked` | `answered` / `approved` / `denied` / `declined` | `respond` (per type table) | `message.<state>` |
| `pending/delivered/acked` | `expired_unanswered` \| `expired_recipient_dormant` \| `expired_recipient_dead` | deadline sweep (§7 cascade) | `message.<state>` |
| `pending/delivered` (notify only) | `displaced` | evicted by a newer notify at mailbox capacity | `message.displaced` (same serial as the displacing send, atomic) |

**Terminal predicate (exact, used consistently by capacity, displacement, inbox,
retention, and GC):**

```
Terminal(m) ⇔ m.state ∈ {answered, approved, denied, declined,
                         expired_unanswered, expired_recipient_dormant,
                         expired_recipient_dead, displaced}
            ∨ (m.state = acked ∧ m.type ∈ {notify, handoff})
```

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
- **Reading never consumes: acknowledgement consumes.** A crash between fsync and
  reply must not lose mail the caller never received, so no read (`inbox`,
  `read_mail`, `check_in`) ever commits consumption. Consumption happens only via
  the recipient's explicit ledgered `ack` or `respond` (see the consumed
  flag above): post-receipt by definition: the client sends it only after it has
  the body. Until consumed, the message keeps appearing in `inbox`/checkpoints
  (idempotent reads); once `Terminal ∧ consumed`, it is GC-eligible **after a
  15-minute consumed-retention window** (erratum E1, found by real-agent
  testing: without the window, GC raced the *sender's* `read_mail` of the
  response: respond marks consumed instantly, and the outcome vanished within
  a sweep tick).
- **Loss is observable, not just ledgered.** When retention caps force eviction of
  *unconsumed* mail (128 terminal/agent, oldest-first), the recipient's replayable
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
- Polling is a **product choice**: MCP 2026-07-28 offers `subscriptions/listen`;
  adopting it is a v1.x option that changes no semantics (the cursor model stays).

## 11. Limits (all enforced; defaults, human-tunable)

| Resource | Default | On exceed |
|---|---|---|
| ops per agent | 10/s, burst 30 | `E_RATE_LIMITED` (no wake, no ledger) |
| live agents / persistent agents | 64 / 16 | `E_AGENT_LIMIT` |
| slots per agent | 32 | `E_SLOT_LIMIT` |
| claims per agent / global | 32 / 256 | `E_CLAIM_LIMIT` |
| mailbox depth (non-terminal) | 256 | §8 backpressure |
| terminal messages retained | 128 per agent, then GC'd (ledger keeps history) | pruned oldest-first |
| archived agents retained in state | 7 days, then GC'd (with nonces + dedup records) | pruned |
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

**Tools (40).** All take `token` except `register`, `resume`,
`hook_poll` and `guard_path` (the last two are lifecycle-hook surfaces and have
no token to give: see SECURITY.md).

The table below is the v1.0 core, and is kept because §12 is the frozen contract
those tools were reviewed against. It is NOT the full surface: v1.1 added blobs
(`put_blob`, `get_blob`), the human/hook surfaces (`hook_poll`, `guard_path`,
`bind_session`, `broadcast`, `all_mail`, `board`), and v1.2 added the
space surface (`open_space`, `join_space`, `read_space`, `post`,
`announce`, `ack_announcement`, `leave_space`, `watch_space`, `admit`,
`evict`, `merge_spaces`, `lock_space`, `unlock_space`) and
`vouch_child`, specified in SPEC-CHANNELS.md, plus `force_release`, the
claim-level counterpart to `unlock_space`.

`tools/list` is the authority, it serves `toolDefs` verbatim, so the served
surface and the advertised one cannot drift. Ask a running daemon rather than
counting a document; this line said 17 for two minor versions.

| Tool | Purpose |
|---|---|
| `register(name, description?, pid?, nonce?, kind?)` | → `{agent_id, token, serial, board}`; nonce required for `kind: persistent` |
| `resume(nonce, resume_id, pid?)` | reactivate a persistent agent: rotates token, bumps activation generation, rebinds PID, wakes, re-arms gate; idempotent per resume_id (§5) |
| `check_in()` | pass the awareness gate (per activation); → atomic `{board, inbox, serial}` checkpoint (§10) |
| `update(description)` / `sign_off()` | lifecycle |
| `heartbeat()` | renew lease while idle (implicit on every call) |
| `declare(slot_id?, text, dirs?)` / `undeclare(slot_id)` | declare/end work units |
| `send(to, type, body, deadline_s?, op_id?)` | → `msg_serial`; `op_id` = durable dedup (§4) |
| `respond(msg_serial, disposition, body?)` | answer/approve/deny/decline |
| `ack(msg_serial)` | explicit read receipt |
| `inbox()` | non-terminal + unconsumed terminal mail, decrypted; marks delivery; returns `truncated_before_serial` (§8) |
| `read_mail(msg_serial)` | full message + response; sender or recipient (§8) |
| `claim(path, mode, note?)` / `release(path)` | §9 |
| `events_since(since_serial)` / `await_events(since_serial, timeout_s?)` | §10 |

**Errors**: structured `{code, message, hint}` tool results (`isError: true`); `hint`
names the corrective action. Codes: the §11 set plus `E_BAD_TOKEN, E_MUST_ACK_BOARD,
E_NO_AGENT, E_NO_SLOT, E_NO_MESSAGE, E_NO_CLAIM, E_MSG_FINAL, E_BAD_TYPE, E_BAD_MODE,
E_BAD_DISPOSITION, E_BAD_NONCE, E_NONCE_IN_USE, E_OP_ID_CONFLICT, E_AGENT_CLOSED,
E_CURSOR_TOO_OLD`.

**Resources**: `dibs://board`. Mailboxes are deliberately not resources.

## 13. Human window

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
for operators who want their own CA or a fronting proxy. Sovereignty is the rule: Dibs
never requires a VPN, an overlay, or an external CA to be safe out of the box.

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
restart grace; limits incl. state GC; MCP 2026-07-28 dual-version surface (41 tools);
local access secret + Origin validation; CLI (board/messages/log/verify/mcp-config);
SSE web board; static binaries (`dibd` + `dibs`, no cgo, no runtime deps).

**v1.1**: rotation + snapshots; kqueue/pidfd exit notification; `dibs watch --exec`
(wake-on-mail supervisor glue); `dibs limits`; `dibs audit`; fuzz + crash
harnesses; `subscriptions/listen`. **v2**: federation; A2A gateway (separate
listener); Ed25519 signatures; hub mode.

Anything not listed is out of scope for v1; additions require a spec revision first.
