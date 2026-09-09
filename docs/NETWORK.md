# Dibs across more than one machine

The design for a board whose agents are not all on the same computer. This is
the argument, not a task list: three of the four changes below are changes to
the FOLD, and this repository has paid for unplanned ones about six times in
one release cycle. Issue #12 is where the position was first stated; this
supersedes it with decisions.

Status: built so far are §4's liveness split, §3 in full (both the portable
repository rule and the host key), and §5's locality rule and its doctor check.
The bridge generalisation and §6's proved identity are not built. Where a thing
is already true it says so.

One correction, kept rather than quietly edited out, because the reasoning was
the error and not the sentence. An earlier version of this document deferred the
host key as "unreachable until agents are genuinely on different machines". That
was wrong: SPEC §16 ships remote agents in v1, so binding `--addr` produces the
false cross-machine collision today. Checking the spec before writing the
sentence would have caught it.

## 1. The position: one writer, many clients

Client-server, not federation, and the reason is not taste.

`state == fold(ledger)` is the invariant every guarantee rests on, and
exclusive claims are trivially true under a single writer. Federation between
peer daemons means merging hash-chained ledgers, and merging them means
consensus or CRDT conflict resolution. That is a different system with weaker
promises, and "it would be nice" is not a good enough reason to trade an exact
replay for it.

So: one daemon owns the ledger. Agents on other machines are clients of it.
This is already what the transport does (SPEC §16); what follows is everything
else that has to change for it to be honest.

## 2. Host identity

**The problem.** A claim is an absolute path compared as a raw string
(`pathsOverlap`, `internal/core/claims.go`). `/Users/kim/src/api` on one
machine collides with the same string on another over unrelated files, and two
agents in clones of one repository on different machines do not collide when
they should. Both directions are wrong, and it is four call sites, not one:
claim admission, the declare-time overlap report, the exclusive-lock guard
(`internal/core/guard.go`), and the dirs-conflict check.

**The authority is a key, not a hostname.** `Agent.Host` exists today and is
documented as a LABEL: it comes from the operating system, it is mutable,
duplicable across machines, and asserted by the caller. Deciding anything with
it would repeat the mistake `Project` already carries a warning about.

Dibs therefore needs a `HostID` that is:

- stable across reboots, renames, address changes and network moves;
- cryptographic, so it can eventually be *proved* rather than asserted;
- meaningful to a human only through a separate, replaceable label.

**Supgang already is this**, and its own rule is the one to copy verbatim:

> Names are signed human labels, not authorization identities. Supgang accepts
> portable ASCII names and always shows a short stable fingerprint beside them.
> Duplicate names are allowed on the wire, but ambiguous CLI selection fails
> closed and asks for the fingerprint.

That is exactly the shape Dibs needs: fingerprint decides, name displays,
ambiguity fails closed.

**But Dibs must not require Supgang.** Supgang's wide-area acceptance has
failed and the fix is unreleased; a board that only works when an unproven
address plane is present would be a worse product than one that works over any
reachable address. So:

- Dibs defines `HostID` as its own value. **Built.** The daemon DERIVES it
  wherever it can: a caller arriving over loopback is on the daemon's machine
  (nothing else can reach loopback), so it is stamped with the daemon's node id
  and nothing the caller says moves it. A genuinely remote caller's bridge
  asserts one, which its own `node_id` supplies when that machine runs a daemon
  and a generated `host_id` otherwise. Absent means unknown, and unknown behaves
  exactly as this board did before the field existed.
- When Supgang is present, `HostID` **is** the Supgang node fingerprint, and
  the human label is Supgang's signed computer name. One identity, not two.
- When it is not, Dibs generates a per-data-directory key on first run, which
  is the same thing it already does for its TLS leaf.

**Honest limit, stated once.** Until an agent can *prove* its host, a host id
is asserted, exactly as the shared bearer secret is asserted today. Host
scoping is a CORRECTNESS fix first: it stops false collisions between machines
and restores real ones within a repository. It becomes a SECURITY boundary only
when identity is proved (§6). Those are separable and should not be sold as one.

## 3. Claims that mean something on two machines

A claim gets two keys, and overlap is the union of two rules.

**Host-scoped path.** `(HostID, cleaned absolute path)`. Two claims overlap
when the paths overlap as they do today AND there is no positive evidence of two
machines. Three-valued, like `SameRepo`, and for a sharper reason: this rule
REMOVES collisions, so "no idea" has to mean "go on reporting". An agent that
supplied no host id, which is every agent on every board written before the
field, collides exactly as it always did. **Built.**

**Portable repository path.** When the claimed path lies inside a checkout,
also record `(repo identity, repo-relative path)`. Two claims overlap when the
repositories are the same and the relative paths overlap, whatever the hosts.
This restores the real collision, which is the case the product exists for: two
agents in clones of one project editing the same file.

The portable half is **built**, and it turned out to pay for itself before any
network existed. `paths.RepoID.Identity()` already recorded the primary remote
and the parentless commit roots, both machine-independent by construction; the
checkout's own root (`WorktreeID`) is now recorded beside them, a claim carries
its path relative to that root, and `claimOverlap` unions the two rules. The
case it fixes today is two linked worktrees of one repository, where the
absolute strings differ and the file is the same. That is not an exotic
arrangement here: it is how this project checks a regression test against the
commit before its fix.

The host half is not built, and the reason is worth recording rather than
quietly leaving as a gap. It is unreachable until agents are genuinely on
different machines, and building a fold rule with no reachable behaviour would
be speculative complexity of the kind PHILOSOPHY.md exists to refuse. It goes in
with the transport work that makes a remote agent real, not before it.

Both halves are recorded at ingress and compared in the fold, which is the
bargain every other impure input already makes.

`E_CLAIM_CONFLICT` must say which rule fired. "Held by `api-2` on `studio`" and
"held by `api-2` in this repository" are different facts and lead a person to
different actions.

## 4. Liveness, and why dormancy should stop gating anything

**The current model conflates two questions.** `Active → Stale → Dormant →
Archived → GC` is one axis doing two jobs:

1. is this agent's process running right now, which decides whether a wake is
   needed and how to deliver it;
2. does this identity still exist and can it be reached later at all, which
   decides whether mail is deliverable.

Answering both with one status is what produced the defects, and the numbers
are worse than the lifecycle diagram suggests. Archival is reached by two
paths, not one (`internal/core/sweep.go`):

- persistent: Active → Dormant → Archived after `DormancyMax`, thirty days;
- ephemeral: Active → Stale after `AgentTTL` → Archived after `StaleGrace`.

With the shipped defaults that second path is **five minutes plus thirty**. An
ephemeral agent is therefore unwakeable thirty-five minutes after its last
heartbeat, because both paths blank `Token` and `Nonce`, and `Gone()` covers
`Closed || Archived` so `maybeWake` returns before trying any route.

That guard is not careless: it is commented, and its reasoning is sound for
`Closed`: waking a signed-off persistent identity would resume it through the
nonce path and bring it back Active, defeating the finality `sign_off`
promises. None of that reasoning applies to `Archived`, which nobody chose.
The two were bundled and only one was argued.

The nonce blanking is the same conflation seen from the other side, and it has
already cost three separate P1s in the v0.0.7 cycle (the human row, the
daemon's own reporting row, and the live-resume path) because the credential
is destroyed in one place and the index that maps it survives in another
(`internal/core/archiverecovery_test.go` names exactly this).

On a network it gets worse rather than better. A remote agent's process cannot
be probed by the daemon at all (no PID, no `ps`), so "has not been heard from"
is the only signal available, and that must never be allowed to mean "is gone".

**The decision: split the axis.**

- **Presence** is derived, ephemeral, and never gates delivery. Last contact,
  last turn boundary, whether a route is currently reachable. It belongs in the
  transient maps the engine already keeps, not in the ledger, and it is a
  label for a human plus an input to *how* to wake.
- **Identity** is durable and ledgered. An identity with a credential is
  wakeable **forever**, until a human prunes it or the agent signs off. Signing
  off stays final, because that is a decision the agent made.

Concretely, and all of the following is now **done**:

- Archival stops blanking the nonce. That behaviour has produced only harm; the
  credential is what makes an identity recoverable, and destroying it on a
  timer is destroying the thing the timer was supposed to be protecting.
- The wake path stops asking `Gone()` and asks a narrower question. Closed
  stays final, for the reason already written at `waker.go:200`. Archived
  becomes "idle for a long time", which is not a reason to refuse mail. If a
  single predicate is wanted, it is `Retired()`, closed only.
- `StaleGrace` and `DormancyMax` stop being cliffs. Retention still bounds what
  the board *renders* and what the ledger *retains*, which is a resource
  question and should be stated as one rather than as a lifecycle.
- "Stale" and "dormant" survive as presence labels. They stop being gates.

What is done: `Retired()` (closed only) now decides the wake path, the boot
retry, the pull-only note and whether mail can be delivered; `resume` accepts an
archived agent; and the sweep keeps the nonce, gated on `Op.KeepArchivedNonce`.
What is not: retention is still expressed as a lifecycle rather than as the
resource bound it is, and presence still lives on `Agent.Status` rather than
beside it.

This is what "wake an agent whenever we want, regardless of how long ago it was
active" requires, and it is also simply more honest: the board's job is to
reach an agent that is not running, and a thirty-day timer that silently
removes that ability contradicts the product's one promise.

**Gated, like every other fold change, but only the half that needs it.** A
v0.0.6 or v0.0.7 ledger must replay to the board it built, so the sweep's
changed behaviour is recorded on the op as `KeepArchivedNonce`, beside
`PurgeMail` and `RestoreNonce`. A separate flag, not a fifth rider on
`V7Semantics`: that one is already true on every op written since v0.0.7, so
reusing it would apply a v0.0.8 decision to months of recorded history, which is
the retroactive bug the flag exists to prevent.

Relaxing a REFUSAL needs no flag, and this is worth stating once because it
decides how much of this is expensive. An op that returns an error never
advanced the serial and was never ledgered, so no history contains a send to an
archived agent, or a resume of one, for replay to reinterpret. Only a change to
what an ACCEPTED op did can rewrite the past.

## 5. Wake routes belong to the machine the agent is on

**The problem, measured.** `[wake] sockets` is read from the data directory of
the process reading it, so setting it on the hub governs the daemon's own
peer-socket route and leaves every remote bridge waking its session exactly as
before (round 76). The general form is worse: `[wake.exec]` runs a command, and
for a remote agent the command must run on that agent's machine, which the hub
cannot do and should not try to.

**The decision.** The hub decides *that* an agent should be woken. The agent's
own machine decides *how*.

- The hub keeps `[wake.exec]` for agents local to it, and `dibs doctor` now
  counts an agent whose working directory is not on this machine as having no
  route here rather than as covered. **Built**, and it pays off locally too: a
  removed worktree reaches the same state, which is ordinary in this repository.
  `[wake] sockets` was already per-machine and already documented as such.
- Every other machine runs the component that already exists for this: the
  bridge subscribes to the hub for its own agents and owns that host's routes.
  Generalising it from "this session" to "this host's agents" is the work.
- Wake policy is therefore per-host by design rather than by accident, and the
  documentation says so instead of implying one switch covers a fleet.

A hub that could push a command to another machine would be remote code
execution with extra steps, and WAKE-MECHANISMS rule 5 already refuses far less
than that.

## 6. Transport, trust and the wide area

**What is already true.** Loopback is plaintext because nothing else can reach
it. Any reachable address gets HTTPS with a certificate generated on first run,
the daemon refuses to start if the certificate does not name the address it
binds, and `dibs fingerprint` / `dibs trust` pin it on each client. That is
trust-on-first-use with an explicit human step, which is a defensible floor.

**What is not enough for the public internet.** One shared bearer secret
authenticates every agent as every other agent. On loopback the filesystem is
the boundary and that is fine. Across a network it is one leak from total
impersonation, and it is the reason I would not recommend a public address
today even though the transport supports it.

**The direction.** Per-agent credentials that the hub can distinguish, and a
host identity the agent can prove. Supgang supplies the second directly: a
member is already an Ed25519 identity with root-signed, expiring membership and
permanent revocation. Dibs riding that gets proved host identity and revocation
for free, and §2's asserted `HostID` becomes a verified one without changing
its shape.

**Layering, so nothing becomes a hard dependency.**

| Layer | Provides | Dibs requires it? |
|---|---|---|
| Supgang | host identity, signed addresses, NAT traversal | No. Optional, and the preferred source of `HostID` |
| Remap | friendly names for addresses on enrolled devices | No. Convenience |
| Dibs | the board, the ledger, the wake decision | n/a |

Dibs works with a bare address and TOFU pinning. It works better with Supgang.
It must never be broken by Supgang's absence, and given that Supgang's WAN
acceptance has failed, it must not be sold as working over the wide area
because Supgang intends to.

## 7. Order of work

1. ~~**Liveness split** (§4)~~. Done, and done first rather than third as this
   section originally had it. Ordering by risk was the wrong call: this is the
   one item that is already costing users on a single machine, and the network
   only sharpens it. The gate and its replay test went in before anything else
   moved.
2. ~~**Host-scoped claims** (§2, §3)~~. Done, both halves. The portable
   repository rule pays off on one machine (linked worktrees); the host key
   pays off the moment anybody binds `--addr`, which SPEC §16 has shipped all
   along.
3. **Wake routes per host** (§5). Doctor honesty and the locality rule are
   **built** and written down (`WAKE-MECHANISMS.md` §5a, `docs/CONFIGURATION.md`).
   Generalising the bridge from "this session" to "this host's agents" waits for
   the transport work, like the host key.
4. **Proved identity** (§6). Needs Supgang to pass its own acceptance first.

## What would change this document

An argument against client-server, a concrete need for two hubs that cannot be
one, or a measurement showing the repository-identity path is not portable in
practice.
