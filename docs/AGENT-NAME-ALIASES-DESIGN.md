# Agent names, aliases and nonce-first recovery

Proposal for request 31579. Source baseline: merged main
`0afbd9d384f5dff8b591dd779f1aa63d57e46dbd`. Accepted by the architect in
33131 at design commit dfbc212; implementation is under review. Stop 342 is merged and normally installed with
verified live build, signatures and retained agent identities. The compiler
window has been returned; this work remains source-only.

## One identity and one mailbox

The nonce recovers one immutable agent id. Names label that identity; messages,
claims, memberships and ledgered recipients remain keyed by id. Renaming never
copies or migrates mail. Each successful rename retains the former name as an
alias until its owner explicitly releases it.

Resolution remains case-sensitive: reserved human/coordinator routing first
where it already applies, then exact id, then current name, then alias.
Preserve the existing live/retired eligibility rules for each caller. A current
name shadows an alias; two eligible alias owners produce E_AMBIGUOUS_AGENT with
sorted ids. Every name-addressed result identifies the actual recipient id.
Use the same resolver for operation references, all_mail's selector and live
configured role pins, respecting each surface's existing authorization.

## Rebuildable alias projection

Keep aliases outside State as a derived index owned by the single writer.
Add name deltas to regenerated agent.updated events: previous/current name,
owner creation serial and released names. The existing ledger replay event
callback and full-history Engine.New input rebuild the index before history is
bounded. Historical updates regenerate their actual prior names without
changing their state transitions or serials. Live updates change the index
only after successful ledger append, before publishing or serving another op.

Index entries bind (id, creation serial), so purging a row and later reusing its
id cannot revive its aliases. Ignore/remove owners no longer retained. Existing
merge_agents mailbox/nonce migration remains explicit; it does not silently
transfer a closed source's aliases to the survivor.

No nonce, token, op history or mailbox body is retained in this index. New
renames are limited to 64 retained former names per row; release some before
adding another. Already-recorded historical names are never truncated on
replay. This bound and release decisions are pure admission rules, not new
replay refusals.

## Explicit release

Add update(release_names: string[]) and the additive frozen op tag
release_names. Shape bounds, blank entries, attempts to release the current
name or an immutable id, and owner checks belong in pure admission decisions;
the engine supplies its derived ownership view to that decision. Releasing
another row's alias is refused. Releasing an already-absent own alias is an
honest no-op; a release-only retry appends nothing and advances no serial.

An effective release is one ordinary update record and one serial. Its
regenerated event removes the alias on live application and cold replay; no
alias set is serialized into State. Mixed rename/release is evaluated against
the resulting names before either mutation, and reports the retained and
released names. Old update ops preserve their previous fold behavior.

## Nonce-first register

Make register's name optional only when a supplied nonce resolves an existing
row. Any supplied current, former or new name recovers that row; omission keeps
its display name. An unknown nonce still needs a name for new registration.
No session id, public name or alias proves identity. Preserve human recovery,
privileged-row, session ownership and invitation guards; a closed identity
cannot be reopened through this path.

Compose existing operations on the writer loop: resolve the nonce, pre-admit
the intended name change, normalize the recovery register to the row's current
name, execute its existing resume/reattach behavior, then authenticated
update(name) if needed. This reuses the existing register retry semantics
instead of inventing a resume_id or changing historical register folds. Return
only after both steps, using the returned token and final serial/board. If a
crash occurs between records, the mailbox stays on the same id; retry completes
the rename. Refused names must not first rotate a token or half-resume a row.

The result says: reattached as <id>; renamed X to Y; X remains an alias, with
any id/current-name shadow explicitly explained. Update no longer tells peers
to abandon the former address. Revise SPEC, SKILLS and its embedded copy,
schemas, nonce hints and CHANGELOG together.

## Proof at the production doors

Real MCP calls and encrypted cold replay must prove former-name delivery,
nonce reattach/rename with one row and intact mail, explicit release,
id/current-name precedence and reported recipient, alias ambiguity, and
restart preservation. Cover active, sleeping and archived recovery; nameless
recovery; rejected rename without partial activation; repeated release; and
creation-serial fencing after GC.

Use a former label different from the immutable id: that id already routes
on old code and would make a false regression proof. Shadowing/ambiguity cases
first establish working alias ownership through real renames. Run each new
behavior guard against old production and record its intended failure; do not
claim existing precedence controls alone fail on old code. Full gate, review,
normal merge and separately authorized installation remain implementation work.
