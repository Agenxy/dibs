# Explicit process and session refresh: design for review

Request 15180, proposal 2. Design only; no refresh implementation is included
in the contact fix. Source baseline: d5ccf05769d86fdd27642545ad295fa95b109a95.

## Problem and boundary

A successful bearer-token call proves the agent identity is coordinating. It
does not prove the caller owns a particular process or harness session, nor
authorise changing where future mail is delivered. Two agents can share one
checkout, so cwd and repository identity cannot select a refresh target.

The contact fix preserves liveness without rebinding anything. Existing
`update(no_process:true)` clears the caller's process record; `release_session`
explicitly clears its session binding. Registration retry is not a reliable
repair because it can return the original registration without applying new
metadata. These mechanisms remain as they are.

## Proposed operation

Use an explicit caller-owned refresh operation, designed for stateless MCP2026.
The request names the agent incarnation and the expected current binding
revision. Omitting a field preserves it; clearing is explicit. It can update
process identity on the same verified host, or replace the current session only
when the new session's ownership is independently verified. It cannot change
roles, mailbox owner, host, cwd, repository or relocation permission.

An agent token authenticates the identity, not the replacement route. A local
bridge must provide evidence obtained from its own harness ancestry/session
source, not from model arguments. The daemon binds that evidence to an
authenticated host/bridge principal and the requesting agent. A trusted-host
assertion without per-agent/session association is insufficient: a host may
serve many agents. Existing `_meta` strings are not made authoritative merely
by being present. If the transport cannot establish this association, refuse
the refresh with a corrective hint; the human/coordinator can use an explicit
authorised repair after reviewing the target instead.

Verification is per request. No initialize session or startup snapshot is
load-bearing. The local host probes the proposed PID and start identity and
verifies its connection to the harness/session. For a remote host, the hub
accepts a signed/bound result from that host's authenticated bridge; it never
probes its own kernel using a remote PID or resolves a remote cwd locally.
Evidence is bound to the exact candidate and agent incarnation, has a short
expiry, and cannot be replayed to refresh another agent. The exact trusted
transport/attestation format needs a separate threat review before code.

## Admission, recording and effects

Resolve/probe outside the core and outside the single-writer loop. Record the
verified inputs, old binding revision, new process/session values and who
authorised the repair in a new op. `Admit` validates stateless payload vocabulary:
shapes, bounds and field combinations. For the new op kind, `Apply` checks
incarnation, expected revision, authority and session conflicts against state,
then folds the recorded verified inputs without re-probing. These new-kind
decisions keep every historical op's semantics unchanged.
New JSON tags are additive and frozen. A genuine no-op does not advance the
serial; an identical idempotent retry returns the prior result.

The state transition replaces the binding atomically. It must not silently
take a session from another active identity. Cross-host moves and replacing a
different agent's route stay separate authorised relocation/repair acts.
After a successful change, derived wake/socket caches are invalidated and
the one-writer-per-session rule is re-evaluated. The response names the exact
fields changed and records the actor; public diagnostics expose status,
never session IDs or credentials. Pending mail is preserved, not auto-read.

## Required acceptance tests before implementation can ship

- Drive refresh through the actual stateless MCP door and actual bridge
  verification; never seed a verified flag in the test.
- A same-host verified harness replacement refreshes only its own row and
  the next sweep probes the new PID/start identity.
- Invalid token, stale incarnation/revision, arbitrary model PID/session,
  unbound host assertion and another agent in the same cwd all refuse with
  complete replayable state unchanged.
- A remote PID equal to a live local PID is never accepted on local evidence;
  lost/replayed/expired remote evidence refuses safely.
- Conflicting active session ownership refuses; same-process PID reuse
  cannot pass the start-identity check.
- Encrypted ledger replay reproduces the accepted repair with no live host;
  old ledgers still fold, no-op/retry serials stay unchanged.
- A real wake after repair reaches the replacement session once, with no
  old socket writer and no mail body in argv.

## Review decision requested

Accept the separation between identity contact and authorised route repair,
and require verified per-agent session association before permitting a
session replacement. Select the trusted bridge attestation mechanism before
an implementation request; bearer token plus ambient cwd is explicitly not
an acceptable shortcut.

## Review record and next step

Architect review 17870 accepted this boundary and supplied the Admit/Apply
correction incorporated above. Its proposed attestation reuses existing
principals: a stdio bridge serves one harness session and measures only its own
parent PID/start identity and session source. Locally, bearer and loopback
host credential must arrive on the same request; the daemon independently
probes PID/start identity before recording. Remotely, the measured claim travels
through the authenticated host-bridge channel and is never probed on the hub.
This is a recommendation for threat review, not an accepted transport design or
implemented feature. The review must explain how daemon verification distinguishes
bridge-measured fields from values a local-secret holder can supply directly;
credential possession alone does not prove a measurement occurred.

Priority remains contact fix, then seen-label provenance. Bring the attestation
back for threat review before requesting any binding-refresh implementation.

## Attestation threat review: request 18594

**Decision: reject recommendation 17870 as sufficient authorization.** Accept
its own-session scope, but keep automatic refresh unimplemented until the
requesting process and its particular session are independently authenticated.
The contact and seen-label fixes are already delivered.

Nothing in `local.secret` prevents its holder from asserting another session's
ancestry. Today's gate compares that shared credential (`cmd/dibd/guard.go`,
`headerSecret`); the daemon accepts TCP (`cmd/dibd/main.go`). An attacker with
their own agent token can submit a real victim harness PID, its real start time
and its real parent chain, then request that **their own** row deliver into the
victim's session. They need not steal the victim's agent token. Probing those
values confirms a process exists; it never establishes that this HTTP caller
is that process or owns the session. Requiring both bearers on the same request
does not close that gap. This is a protocol counterexample, not a live attack
performed against another agent.

An acceptable replacement needs both of these proofs:

- **Caller origin:** obtain process identity from the request transport's
  kernel-authenticated peer, not JSON, headers, cwd, environment or a bridge's
  assertion about itself. Independently verify its process generation and
  exact permitted harness relationship against an already verified binding;
  arbitrary descendants and a newly asserted parent are insufficient.
- **Session ownership:** bind that peer to the exact agent incarnation and
  harness session using an independently established session association.
  A shared app/ancestor may host several sessions. Ancestry alone cannot select
  among them, and same-user writable sidecars or inherited environment are
  observations, not adversarial authentication. If the harness cannot provide
  this association through protected IPC or an isolated session capability,
  refuse automatic session replacement and use explicit authorized repair.

Unix peer credentials are a candidate input, not a completed design. Apple
defines `LOCAL_PEERPID` and `LOCAL_PEERTOKEN` in
[XNU's local-socket interface](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/un.h).
Linux's [SO_PEERCRED contract](https://man7.org/linux/man-pages/man7/unix.7.html)
reports credentials at connection creation. A design must therefore settle
socket delegation, connection reuse and process-generation races; UID alone,
a socket pathname or a once-per-initialize check is insufficient. Measure the
actual supported harness/OS door before accepting it.

Remotely, the authenticated host channel must carry the **host's verified
caller/session binding**, not merely repeat caller-supplied ancestry. Bind the
proof to host, agent incarnation, expected binding revision, exact replacement
and a fresh challenge with expiry/replay rejection. The hub never probes remote
PIDs locally. Contact-only TCP requests retain their existing behavior.

Scope is another process possessing the shared host credential; compromised
kernel or trusted verifier is outside it. If untrusted same-UID programs can
modify the session association or control the attesting process, an independent
OS isolation boundary or explicit repair authorization is required. Adding a
bridge secret in that same readable trust domain would not supply the missing
boundary. No new credential or refresh code is authorized by this note.

Before acceptance, require real-door refusals for a direct caller claiming a
real victim chain, a nested child, a sibling session under one app, delegated
or stale connections, PID reuse and replayed remote proof. Proposed mechanism
and this decision remain subject to architect review.
