# Supported agent merges

Design checkpoint for request 138494 and its session-identity addition 142249.
Base: `1a5ea8212691bac3ae4827fb3d87e242e7064fa1` (PR #449).
Status: proposed; no implementation or production merge is claimed.

## The Supgang cause

The duplicate predates the directory-alias recovery fix in PR #373:

| Evidence | Observation |
| --- | --- |
| Supgang session registration, 2026-10-03 06:07:13.373Z | Caller supplied `<checkout-parent>/SupGang`. |
| Same session registration, 2026-10-05 12:18:23.567Z | Caller supplied `<checkout-parent>/Supgang`, the original role name and session ID, without a nonce. Result at 12:18:23.660Z named `gpt-supgang-dev-0-2`. |
| Retained credential-cache metadata | The two legacy spellings retain two distinct credentials. The canonical `dir:v1` entry selects the original credential. No secret was printed. |
| Native filesystem observation on 2026-10-15 | The two paths name the same directory (`os.path.samefile`). This is current evidence, not a historical filesystem snapshot. |
| Historical source before #373 | The cache key was the exact project spelling plus name. A miss minted a new nonce. A supplied nonce prevents session-ID recovery, correctly preserving the stronger credential boundary. |
| Daemon log, 2026-10-05 19:59:18.936-07:00 | After #373, retained credentials resolved to the oldest identity, explicitly naming the original and duplicate. This repeats on October 7, 9, 12 and 14. |

The measured sequence and retained cache explain this fork: an alternate native
case spelling missed a retained credential, then the bridge's new credential
identified a new participant. Losing the model's transient store exposed the
cache defect; trusting the public session ID would weaken recovery authority.

Prevention already exists in #373: native filesystem spelling at the edge,
legacy-cache retention and same-file grouping, bounded credential resolution on
the writer, oldest proven identity selection, and refusal without minting on
unproven recovery. No new registration rule is justified by this incident.

The permanent production-door guard
`TestIdentityRecoveryRealStdioNativeCase` passes with `-race` on this base.
Restoring both pre-373 decisions through a local source overlay (exact-key-only
nonce lookup and canonicalization without native spelling) makes that same test
fail at runtime: `same native directory forked an identity: worker -> worker-2`.
This is a decision mutation on current source, not a whole historical checkout.
An exact-key-only mutation still passes because native spelling independently
prevents a new fork. A first control failed compilation from an unused import;
that was corrected before obtaining the runtime failure and is not counted as
the negative proof. Registration and the cache on the installed board were not
changed. The orphan remains for the architect's eventual production acceptance.

## Authority and one decision

Add one pure `core` decision used by the engine and board presentation. It takes
the two rows plus explicit process-evidence values, and returns eligibility and
stable refusal reasons. It requires:

- Different, existing rows; a usable survivor.
- Equal nonempty names, canonical host IDs, harnesses and surfaces. Unknown
  identity is insufficient evidence for coordinator authority.
- A non-active source, zero declarations, and no stored session binding
  (primary, alias or current). A dormant status alone proves none of these.
- No live source process. No recorded PID means there is no process binding;
  an existing PID requires a current local negative probe. Remote or unknown
  evidence refuses coordinator execution. No PID on another host is probed.
- Strictly later durable `LastCoordination` on the survivor. Equality refuses;
  the ID suffix is never used to pick a direction.

Coordinator execution is allowed only on this decision. Member execution is
refused. Admin retains its existing authority. Human execution uses the board's
existing authenticated human identity, never a caller-supplied `human` string.
Preview metadata does not grant access to either mailbox's content.

Process observations occur outside core, then the writer checks that the row's
incarnation, host, PID and process start still match the measured binding. The
committed op records the evidence and actor. Commit repeats the authority,
binding, eligibility and fence checks inside the writer; it cannot be a query
followed by an independently queued tokenless `Do`, as the present admin wrapper
is. If a local probe cannot distinguish an absent process from an observation
error, add that distinction at the probe edge rather than treating every false
boolean as absence. A process can change after a measurement; this does not
claim OS-level exclusion against a concurrently starting process.

**Review decision needed:** interpret “the human may merge any pair” as allowing
the human to override the coordinator identity, session, recency and declaration
rules, with every difference shown in the preview. Preserve refusal for an
active source and conflicting claims for all callers in the first version.
The current fold refuses an active source, and dropping conflicting claims would
lose ownership evidence. If live-source override is intended too, it needs an
explicit new recorded semantic decision and preview of token revocation; it
must not silently relax the historical guard. No such override is implemented.

## Preview, fence and execution

`merge_agents(token, agent, into, dry_run:true)` returns a metadata-only plan:
source and survivor IDs/incarnations, eligibility reasons, incoming messages by
kind/state, durable owed requests separately (a subset, not an added count),
outgoing address rewrites, claims, memberships, and the number of nonce mappings
that follow. No bodies, credentials, attachments or space posts are returned.
Preview accounts for mailbox watermarks and inherited readable mail explicitly.

The response includes `fence_serial` plus an opaque `preview_id`. Commit supplies
both, with the same ordered IDs and authenticated actor. New real calls without
a preview receive a corrective dry-run hint; admin authority remains available.
Wrong caller, swapped pair, lost cache, expiry or stale inputs require a new
preview. The fence is not a credential.

A global serial equality check would reject unrelated fleet traffic. A hash of
the current rows alone would accept change-and-revert. Proposed implementation:
at most 16 short-lived preview leases, one per actor and ordered pair, expiring
after ten minutes. They are an engine-only derived cache. Each stores its
creation serial, incarnations and current fingerprint; no mailbox copy is kept.
Replacing or evicting a lease makes its old ID unusable. Restart loses previews
and requires a new dry run, without losing coordination state.

While leases exist, the common successful-append path recomputes their pure
merge-input projections after every committed op, before publishing events.
Any change permanently invalidates the affected lease. Thus two separate ops
that change and restore a row still invalidate it. Failed or non-mutating ops
do not. Unrelated rows, messages and space posts do not enter a pair's
projection. An invalid lease stays invalid; it is never refreshed by comparison
with a subsequently matching state.

The projection covers both complete replayable row records, incoming mailbox
records and ownership watermarks, outgoing message IDs/address relationships,
owned claims, relevant membership records and nonce mappings. Private inputs
are hashed internally and never returned. Outgoing message content and delivery
state do not change an address rewrite and are not preview inputs. Current
process evidence is checked afresh at commit. Ephemeral owner/app observations
are neither serialized into core implicitly nor mistaken for durable contact.

This deliberately avoids maintaining a new dirty-row table in every mutation
handler or inferring mutations from event names. It costs projection work while
a preview is pending. Measure with a full retained board and all 16 leases;
report memory and writer latency before accepting this implementation. If that
cost is excessive, revise this design before substituting a partial op switch.

## Human request whose approval performs the merge

Introduce a typed `merge` effect on a request: exact source/survivor IDs and
incarnations, plus the preview binding. It is mutually exclusive with grant and
adopt effects and can be addressed only to the authenticated human. A refused
coordinator call offers this concrete `send` shape; it sends nothing itself.
An ordinary participant may ask for a human decision without gaining execution
authority. Dedup includes the complete effect. Retargeting, changing pair/order
or reincarnating either row cannot reuse an earlier approval.
Extend the existing pending-effect recovery protections to this effect too:
public-name/session-only recovery must not take an identity while a human
decision is pending, or after merged private mail has landed on it. Possession
of a public thread ID is never permission to inherit that mail.

Creating the request verifies the supplied preview first, then records the
typed effect and starts the approval lease at the request's committed serial,
atomically on the writer. This ordering matters if the requester is one of the
pair: sending its own control request changes that row. The resulting
notification/board preview is generated from the post-request state. Merely
delivering the outgoing request does not alter its address-rewrite inputs.
There is no general exemption for unrelated mutations.

The OS notification and board card name the direction and actual effect from
typed fields, not sender prose. Human Approve verifies the lease and current
rows before any message state changes. One pure transition both records approval
and performs the move, with one serial and one ledger append. An expired/stale
approval changes neither mail nor response state, and offers a fresh preview.
No successful approval means “now run a terminal command.” Notification posting
still proves OS acceptance only, not that the person saw it.

The board's fork-row button opens the same preview and executes through the
same engine entry point using its existing cookie/page-key/CSRF protection.
Cookie-only or forged human identity cannot execute it. The MCP Apps panel
continues to speak as its own agent; viewing it does not confer human authority.

## Fold, ownership and audit

Add `merge_actor` as a new frozen op tag containing the actor ID, incarnation and
authority at the decision. New evidence/effect fields are additive too. Never
rename `merge_into`, reuse `adopt`, or backfill actors on historical merges.
Persist the resulting source-row audit record and event: destination, actor and
time. Closed merged rows need a bounded history/detail presentation, since the
current `Board()` hides closed rows. The board says “merged into X by Y at T”;
historical actor absence is shown as unknown.

Ingress supplies all authorization fields; clients cannot assert them. Existing
ops without new fields replay with their original behavior. New admission rules
stay at ingress; new fold behavior is selected only by an explicit new recorded
semantic field when needed. Replay never probes, reauthenticates a human, checks
a preview cache or recomputes a past actor's role.

Adopt remains one-time recovery of currently recoverable incoming mail. Merge
closes a duplicate permanently, rewrites the relevant outgoing addresses, moves
claims/memberships and redirects its nonce mappings. Share the ownership-aware
mail selection/move primitives with explicit options, preserving historical
adoption and merge folds. The current merge's raw address rewrite does not mark
older mail as inherited, so tests must prove the survivor can actually read
moved mail below its creation watermark, without exposing a predecessor's
excluded mail. Plans and execution use the same selection function.

The survivor's own role, identity, process and session bindings remain its own;
merging is neither adoption of the source's authority nor session relocation.
The source token is revoked, its nonce mappings follow the survivor, and moved
outstanding obligations emit actual arrival events so a dormant survivor wakes.
The adopt hint offers merge only when the shared coordinator rule passes.

## Row identity and fork presentation

Full detail and the human/MCP panel expose bound primary, alias and current
session identifiers, each paired with the row's host ID/label. Show the current
app thread only when the recorded binding establishes one; retain provenance
for guessed bindings. A move must display the new current thread, not the first
historical alias. No socket paths or credentials accompany these identifiers.
The default compact roster stays compact.

Flag same-name rows as possible forks using host/harness/surface evidence,
without calling every same-name participant a duplicate. Present suggested
direction and refusal reasons from the shared decision; browsing the board
does not authorize a move or create a preview lease for every row.

## Acceptance evidence to produce after design approval

- Actual MCP registration through the stdio bridge for native-case recovery;
  retain the existing #373 guard and its pre-373 decision control.
- Both protocol versions: passing coordinator preview/commit, reversed pair,
  member and unknown-host/harness/surface refusals. Drive registration and
  declarations through production entry points, not matching fields set by hand.
- Bound-session, live-process and observation-error refusals; a binding changed
  during a probe; role revoked between preview and commit; forged actor fields.
- Stale fence on each row, incoming mail/owed debt/claim/membership/nonce change,
  change-and-revert, concurrent commits, swapped pair, wrong actor and restart.
  Unrelated fleet traffic and ordinary outgoing delivery must still allow it.
- Human request delivery and notification effect, approve-to-move in one op,
  stale approval with no partial response, effect dedup and mutually exclusive
  grant/adopt/merge fields. No credential or body in preview/notifications.
- Read moved old mail through the survivor's real `read_mail`; predecessor
  watermark privacy, debt completion, nonce re-register and wake after move.
- Replay old merge/adopt fixtures unchanged; new actor and audit survive replay;
  exactly one serial/append for direct merge and effect approval.
- Browser e2e through fork button, preview and human confirm, rejected stale
  preview, post-merge audit, session detail and moved current-thread identity.
- Runtime negative controls for every new behavioral guard, then full local
  `task ci` and fresh exact-head macOS/Linux/Windows validation. No production
  merge/install from the implementation agent; architect runs the Supgang pair.

Review checkpoints: confirm the human override boundary above, approve or revise
the bounded observation-based fence, and confirm the additive typed request
effect and post-request lease baseline. Implementation follows their acceptance.
