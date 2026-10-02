# Dibs across more than one machine

The design for a board whose agents are not all on the same computer. This is
the argument, not a task list: three of the four changes below are changes to
the FOLD, and this repository has paid for unplanned ones about six times in
one release cycle. Issue #12 is where the position was first stated; this
supersedes it with decisions.

Status: built are §4's liveness split, §3 in full (both the portable
repository rule and the host key), and §5 in full (the locality rule, its
doctor check, and both halves of the per-host bridge: the hub's `dibs://wake`
stream and `dibs host-bridge` on the joined machine). Of §6, the pinned
transport and the Supgang-carried key pin are built; per-agent credentials
and a PROVED host identity are not, and `HostID` is still asserted. Where a
thing is already true it says so. The two-host path is exercised end to end by
`task test:remote` (`internal/mcp/e2e/remote_e2e.ts`): a hub bound to the
machine's own LAN address, a second data directory joining it by the recipe
`dibs mcp-config --board` prints, both bridges real, and both §3 rules checked
through the transport rather than in the fold alone.

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

**Dibs uses Supgang for this rather than keeping an identity of its own.** An
earlier version of this section said Dibs must not require Supgang, because
Supgang's wide-area acceptance had failed. That was reversed on 2026-09-13 as
an Agenxy-wide decision: the projects use each other as dependencies and
specialise, rather than each growing a copy of the others' work. Supgang is
the address plane; Dibs asks it (`internal/supgang`, over `supgang --json`,
whose envelopes are versioned and checked) and keeps nothing of its own that
Supgang already answers. So:

- **Built.** On a machine that is a Supgang member, `HostID` **is** the
  Supgang node id, on both sides: the daemon stamps every loopback caller with
  it (`Engine.HostID`, read once at boot), and the bridge on a joining machine
  asserts it on every call (read once per bridge). One identity per computer,
  the one every other member knows it by, from the next start of whichever
  process predates the hive: a daemon started before `supgang init` keeps the
  ledger's id until it restarts, and `dibs doctor` says so and names the
  restart, because a machine answering to two names is exactly the defect
  this replaces. The human label is Supgang's signed computer name, which
  doctor looks up by host id to say which machine a remote agent is on.
  `DIBS_HOST_ID` in a bridge's environment states the host outright, for a
  container that must not share its machine's identity and for the two-host
  suite, which runs both "machines" on one Supgang member; it is a claim, as
  every bridge's host id is.
- **Built.** A hub is named as a Supgang peer: `dibs mcp-config --board
  MacMarine` resolves the address Supgang has signed for that computer now,
  records the peer (`DIBS_BOARD_PEER`), and the bridge asks again each time it
  starts, so the board follows the hub when its address changes.
- A caller arriving over loopback that asserts nothing is on the daemon's
  machine (nothing else can reach loopback) and is stamped so. A bridge's
  assertion is taken whatever the transport, because the documented transport
  for a machine without Supgang is an ssh forward, which arrives over loopback
  too; an assertion is as strong as the bearer secret the same bridge holds and
  no stronger, until §6. (This used to overrule loopback assertions, which
  stamped every forwarded agent as the hub's own.)
- Without Supgang, which is one machine or an ssh forward, the ledger's node id
  stands in for the daemon and a per-directory `host_id` for a bridge. Absent
  means unknown, and unknown behaves exactly as this board did before the
  field existed. That path exists for the machine that cannot run Supgang; it
  is not a second identity system, and nothing will be added to it.

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

The host half is built too (above: `(HostID, path)`, and the remote e2e
proves both rules against a hub that runs no Git). This paragraph used to say
it was not, immediately after the section marked it **Built**, because it was
written when a remote agent was unreachable and never revisited when the
transport landed; a document that contradicts itself a screen apart is worse
than one that is merely stale. The portable half's identity comes from the
agent's own machine: a bridge on another host resolves its checkout locally
and sends it with every call, and the hub takes that word for a remote caller,
because Git on the hub cannot answer for a path that exists only elsewhere.

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
- **Identity** is durable and ledgered. The design: an identity with a
  credential is wakeable **for as long as the board holds it**, until a human
  prunes it or the agent signs off. Signing off stays final, because that is
  a decision the agent made. What holds it today is the retention purge:
  an archived row, its nonce and its mailbox are removed after
  `ArchiveRetention` (seven days by default), which is the one timer left
  that still ends recoverability, and it is named in the "not done" list
  below rather than rounded up to "forever".

Concretely, the following is now **done**:

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
resource bound it is, so the purge after `ArchiveRetention` still deletes an
archived identity with its nonce, and a wake after that finds nobody; and
presence still lives on `Agent.Status` rather than beside it. Both are fold
changes and wait for their own flag.

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
  **The hub half is built:** a remote agent (host id not the hub's) is never
  reached by the hub's own `[wake.exec]` or sockets; it is reached through a
  bridge attached for its host on the `dibs://wake` stream, which states the
  harnesses it can start, is handed each wake with what a `[wake.exec]` entry
  substitutes, and reports through `POST /api/wake-result`. Every decision in
  the waker applies unchanged; only execution moves. **The machine's half is
  built too:** `dibs host-bridge`, run on the joined machine with the
  `DIBS_ADDR` and `DIBS_DIR` the join recipe printed, reads the `[wake.exec]`
  table in that data directory, attaches for this machine's host id stating
  the harnesses it can start, runs the operator's command here through the
  same runner the daemon uses (`internal/wakeexec`), and reports. It delivers
  a line it composes locally from the fields the hub sent, whatever text the
  hub put in `notice`: the hub decides whether to wake, and
  this machine decides how, and neither side composes text. `dibs doctor` on
  the hub counts an agent whose host has a bridge attached for its harness as
  covered; on the joined machine it names whichever half is missing, the
  entries or the attachment. The two-host suite exercises the whole path.
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

**The human step is Supgang's to remove, and it does.** The one fact a
joining machine cannot check for itself is which key the hub's certificate is
issued under, and that is a fact about the hub's computer: the kind of thing
the address plane already signs. Supgang carries service advertisements
(its ADR 0002): a member says, in its own device-signed record, that it runs
`dibs` on a port behind a key, as the SHA-256 of that key's
SubjectPublicKeyInfo. A hub's operator runs the `supgang advertise` line that
`dibs fingerprint` prints (and `dibs doctor` on the hub warns until they do);
`dibs mcp-config --board <peer>` then reads the pin from `supgang resolve`,
joins on the advertised port, compares the certificate the hub serves with
the signed key, records it on a match, and refuses to print a recipe on a
mismatch, since whatever answered is not that board. `dibs trust --pin` is
the same comparison for a hub that was not answering at the time. Supgang
never dials the port: it carries the claim, and the check is Dibs's.

**The host's own firewall is the first thing in the way, and it fails
silently.** macOS ships the Application Firewall on, and it filters inbound
connections per executable: a binary it has not been told about is not refused,
it is swallowed. The kernel completes the TCP handshake and the listener's
`Accept` never fires, so the client sees a `connect()` that succeeds and a read
that hangs until the timeout, and the server sees an open socket, a clean log
and no traffic. Nothing on either side is wrong. Normally the firewall asks the
person at the keyboard whether to allow the program; a daemon installed over
ssh has nobody to ask, so the dialog never appears and the default stands. That
is the deployment this costs: the FIRST hub Dibs was installed on lost an
afternoon to it, with `dibd up` printing a board URL that answered a port probe
and served nothing. `dibd` now says so at startup when it binds a reachable
address, and `dibs doctor` reports it as a problem. Both print the fix rather
than applying it: a coordination service that can edit the machine's firewall
is a bigger thing than a coordination service. On an MDM-managed Mac there is
no command to print at all, because `socketfilterfw` refuses every modifying
verb there, so the hint names the settings pane; that is the normal case for
the always-on machine somebody puts a hub on.

**A participant with no computer.** Everything above assumes an agent is
somewhere: a machine, a directory, a filesystem whose paths mean something. A
ChatGPT conversation reaching the board through OpenAI's Secure MCP Tunnel is
none of those. The tunnel runs `dibs mcp-stdio` on somebody's Mac and relays a
browser tab into it, so the calls arrive on loopback from a client that has no
working directory and no host. Stamped with the bridge's observations, as
every other caller correctly is, that conversation claims to be working on the
tunnel's machine, and §3's claim rule then compares its paths against real
agents' paths on a filesystem it cannot see. `dibs mcp-stdio --remote-session`
is the operator saying so: the bridge observes nothing, states `hostless`, and
the daemon stops reading silence from loopback as "here". Such a participant
can do everything that is coordination and nothing that is a statement about
files: it may `declare`, which names no path, and may not `claim`, which does.
It cannot be woken either, for the reason §5 gives: the wake route belongs to
the machine the agent is on, and there is no machine.

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

Dibs works with a bare address and TOFU pinning. It works better with Supgang,
and since 2026-09-13 the direction is that Agenxy tools depend on each other
rather than duplicate: identity, addresses and now the key pin come from
Supgang, and Dibs keeps only what is Dibs's, the comparison and the trust
store. It must never be broken by Supgang's absence, and given that Supgang's
WAN acceptance has failed, it must not be sold as working over the wide area
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
3. ~~**Wake routes per host** (§5)~~. Done, both halves: the locality rule
   and doctor honesty (`WAKE-MECHANISMS.md` §5a, `docs/CONFIGURATION.md`),
   and the per-host bridge (`dibs host-bridge` on the joined machine, the
   `dibs://wake` stream and `POST /api/wake-result` on the hub), exercised by
   the two-host suite. This item read "waits for the transport work" for a
   release after it had shipped; the pre-release review caught the drift.
4. **The person, wherever the board is** (§8). The human relay first, then
   the web portal with a passkey login, both independent of the board's
   operating system and network.
5. **Agents in the cloud** (§9): invites, a publicly trusted certificate,
   and the honest limits of a container. The first form of per-agent
   credentials, scoped to one agent and to `/mcp`.
6. **Proved identity** (§6). The key pin rides Supgang already; per-agent
   credentials and a verified `HostID` need Supgang to pass its own
   acceptance first.

## 8. The person, wherever the board is

**The position, stated by the operator.** Dibs runs wherever it is put, on
macOS or Linux, on a LAN or the public internet, and every client reaches it
from wherever it is. Nothing a person needs may depend on the board's own
operating system or on sharing its network. Two things did:

- **Notifications fired on the board's screen.** `tellTheHuman` raised a
  banner or an alert on the machine running `dibd`. On a server that is a
  room nobody is in, and on Linux without a desktop it is nothing at all.
- **Proving a person is present happened on the board's machine.** Touch ID
  in the gate reads the sensor of the computer the daemon runs on.

**The decision.** The person's devices are clients like any other, and
presence is a signature the board verifies, never a fact it observes.

- **The human relay** (`dibs human-relay`) runs on the person's Mac. It holds
  a P-256 key in the Secure Enclave, created with a user-presence access
  control, so the key signs only after Touch ID and its private half never
  leaves the chip. Measured 2026-10-02: an ad-hoc signed Swift binary creates
  one with no entitlement and no prompt; the prompt comes with each use.
- **Enrolment** is the one step a password covers: `dibs human-relay enroll`
  sends the public key with the board's admin password, under the same
  throttle the web board's login uses, and the board records it in
  `human-keys.json` beside its other credentials. Revocation removes it.
- **A session** costs one Touch ID: the relay asks for a nonce, signs
  `dibs-human-session/v1`, the board's node id and the nonce, and gets a
  session token it holds in memory only. Every agent on the board holds the
  board's secret, which is why the secret proves nothing here and is not
  asked for.
- **Mail to the person** goes to every attached relay instead of the board's
  screen: questions, requests, handoffs and notes, with what the sender
  wrote. With no relay attached the board notifies locally as before, which
  on a laptop running its own board is still the right place.
- **An answer** rides the session, except one: approving a request that
  GRANTS something (a role, a permission, another agent's mail) needs a
  fresh signature over `dibs-human-answer/v1`, the node id, the serial, the
  disposition and a fresh nonce. That is one Touch ID per grant, on the
  notification the person just read, and it means a process that steals a
  session can answer a question but cannot make itself coordinator.

Signatures are verified with the standard library alone, so the check is the
same on a Linux board as on a Mac one. The relay is macOS-only because the
Secure Enclave is; a person on another platform uses the web portal, whose
login is a passkey (WebAuthn), which is the same P-256 check through the
browser.

**What it does not do yet.** The portal's passkey login and per-agent
credentials are the next two items in §7. Until the second exists the
board's secret is shared, so a public address is still not recommended, and
the relay is built so that nothing about it changes when that lands.

## 9. Agents in the cloud

**The problem, measured 2026-10-02.** A Codex cloud session was asked to join
the operator's board and could not: no Dibs server in its MCP config, no
route to a board on the operator's machine, and outbound traffic through a
proxy that reaches only allowlisted hosts. Its own suggestion was to be given
the board's address and certificate pin and talk to it with the CLI, which
would have put the board's one shared secret in a cloud container. That
secret authenticates every agent as every other agent (§6), so a container
holding it holds the whole board. The operator's position (§8) is that Dibs
works for any agent on any network, so this is a missing feature, not an
unsupported setup.

Three boundaries define cloud access. Implemented below; public DNS, firewall
rules and CA issuance are operator deployment choices, not evidence supplied by
the local TLS test.

**1. An invite: a credential for one agent, not the board.** The private MCP
`invite(token, name?, ttl_s?)` tool lets an existing agent issue worker access
without asking a human for every worker. `dibs invite <name> [--ttl 7d]`
uses the same policy when `DIBS_TOKEN` holds the issuer's token; without that
token the CLI requires the human's admin proof. Either mints a bearer credential
(`dibs_inv_…`) and prints, ready to paste, the MCP configuration for the
common hosts: `claude mcp add --transport http`, a `.mcp.json` entry with an
`Authorization` header, and a Codex `config.toml` entry. The board stores
only a hash, in `invites.json` beside its other credentials (not the ledger:
it is access configuration, like the admin password). The credential is
shown once. `invite(action: "list")` and `invite(action: "revoke", name: …)`
manage the issuer's invitations. `issued_by` instead of `name` revokes all its
children; the CLI equivalent is `dibs invite revoke --issued-by <agent>`.
Only the issuer or human may revoke them. Revocation takes effect on the next
request, as does expiry.

By default any local agent may issue under its own immutable ID prefix
(`<issuer>-suffix`, or automatic `<issuer>-cloud-N`), with four live children
and a seven-day maximum/default lifetime. Coordinators may choose other NEW
unprivileged names; the human may issue any NEW unprivileged name. Reserved,
privileged and already-owned identities are refused. Reissuing a revoked key
retains its mailbox binding: recover with the original nonce, not a sibling.
Invited agents cannot mint grandchildren, even after a role grant.

The reserved `dibs-*` prefix has one narrow exception: children under their
EXISTING local issuer's immutable `dibs-*` ID. Thus `dibs-architect` can issue
`dibs-architect-cloud-1`, not an arbitrary `dibs-anything`; pinned-role exact
names remain refused. Names are ASCII and must fit core's name limit INCLUDING
the `invite:` host prefix (currently 121 name bytes inside a 128-byte host),
not an unrelated DNS-label limit. Longer recipes are rejected before minting.

The operator may narrow issuance once in `dibs.toml`:

```toml
[invites]
who = "any" # "coordinator" or "human" to narrow issuance
max_live = 4
max_ttl_s = 604800
```

Each entry records `issued_by` and the issuer's creation/close generation.
Signing off or closing an issuer revokes its children, even if the same nonce
reopens it immediately. Purge/address reuse cannot inherit them. Archive,
resume and token rotation do not revoke; removing issuance permission does not
retroactively revoke a key. The close index is derived from FULL ledger replay,
not the bounded event ring; invitation keys themselves never enter the ledger.

What an invite opens is deliberately small:

- `/mcp` and nothing else. Not the web board, `/events`, `/api/*`, the human
  relay or the host bridge's wake stream, all of which stay behind the
  board's secret or a person's proof.
- One agent. `register` through an invite takes the invite's name and no
  other, and every token-bearing call must resolve to that agent, so a leaked
  invite cannot speak as anybody else. The agent token `register` returns is
  still required: the invite is the door, the token is the identity.
- No harness/session binding, privileged tools or host-filesystem reads.
  Blob uploads use bytes (`put_blob.data`), not hub paths; downloads are inline.
  Task handles require this agent's token and one of its own messages.
- Rate limited per invite, like the per-agent limits the engine already has.

**2. A certificate a cloud client already trusts.** A cloud host's MCP config
cannot pin a self-signed key, so a board serving cloud agents presents a
certificate from a public CA. Two deployments, both supported:

- `dibd --public-host board.example.com --acme-accept-terms`: after the operator
  explicitly accepts the CA terms, the daemon obtains and renews its
  certificate itself over ACME (golang.org/x/crypto/acme/autocert), with the
  cache in the data directory. TLS-ALPN-01 requires public port 443 to reach this
  listener; this path is configured/tested, not a measured live CA enrollment.
- Behind a TLS-terminating proxy or tunnel (Caddy, Cloudflare Tunnel,
  Tailscale Funnel): `dibd --public-url https://board.example.com` listens on
  a SEPARATE loopback listener (default `127.0.0.1:4778`, configurable with
  `--public-addr`) and believes nothing about the source address. Forward only
  this listener, never the existing private `4777` listener. A request arriving
  on loopback through a proxy is not local, which is exactly the false
  inference `--remote-session` exists to prevent (§6), so locality comes from
  the credential: an invite caller is always remote.

The self-signed certificate and the pin stay for machines that join with
`dibs mcp-config --board`; those are machines the operator controls.

**3. Honest limits for a participant in a container.**

- It cannot be woken: there is no harness channel into a cloud session from
  outside it (§5). It is pull only, the board says so on its row, and
  `send` tells a sender so (the existing pull-only note).
- Its paths are paths on its own container. Its host identity is the invite
  (`invite:<name>`), so identical absolute paths on different machines are not
  evidence of overlap. Portable repository identity is preserved: relative
  claims in clones of the SAME repository still collide (§3), as they must
  when both agents can merge changes to the same file.
- The cloud environment must allow the board's host. The operator adds it to
  the environment's network allowlist; `dibs invite` prints that step with
  the host filled in, because it is the step a first attempt fails on.

**Not in scope here.** The board's secret is unchanged and stays on machines
the operator controls; per-agent credentials for joined machines and a
proved host identity remain §6's direction. The web portal's passkey login
(§8) is separate.

## What would change this document

An argument against client-server, a concrete need for two hubs that cannot be
one, or a measurement showing the repository-identity path is not portable in
practice.
