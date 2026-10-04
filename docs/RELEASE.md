# Gate before tag, then resumable publication

A release starts with `workflow_dispatch` of `release.yml` on protected `main`,
with a canonical version and full candidate SHA. The candidate must be main's
current tip and its committed release surfaces must already name that version.
The preflight has read-only repository permissions. It creates an annotated tag
**locally**, runs the entire `task ci`, checks the authenticated Sigstore root,
and builds an offline GoReleaser snapshot with the actual release version.
Nothing is pushed or published by this job. Failure can be repaired and the
same version dispatched again.

A separate job, behind successful preflight, revalidates the candidate and
pushes only that annotated tag. Release tags remain immutable. It never pushes
main or unrelated tags. An existing tag is accepted only at the exact SHA.
The immutable Actions receipt binds repository, workflow run/attempt, workflow
SHA, candidate SHA, tree, version and successful preflight. It is not trusted
because a file says so: publication queries the Actions API to authenticate its
origin and successful run and downloads the run's artifact itself.

A completed-success finalizer has only read access and Actions dispatch
permission. It reads that authenticated receipt and dispatches `release.yml`
at the tag. The publisher checks the receipt again. This preserves the existing
Sigstore certificate identity, `release.yml@refs/tags/<exact-tag>`; signing from
main or accepting arbitrary main-workflow signatures would break installed
clients and widen the trust contract. A hand-pushed tag triggers nothing.

Both jobs recreate the identical annotated tag object: fixed tagger identity,
annotation and candidate commit timestamp. The receipt binds that object ID,
not just the commit underneath it. Publication refuses a different annotation
even when it peels to the same candidate.

After the owner approves the actual release, dispatch from main:

```text
gh workflow run release.yml --ref main -f mode=preflight -f version=0.0.11 -f sha=<full-current-main-sha>
```

If the finalizer or a downstream service fails, authenticate the successful
preflight run and retry publication without rebuilding a public release:

```text
gh workflow run release.yml --ref v0.0.11 -f mode=publish-only -f version=0.0.11 -f sha=<proven-sha> -f preflight_run=<successful-run-id>
```

Receipts are retained for 90 days. An expired or missing receipt is not silently
trusted: repeat a successful preflight on the same candidate (which must still
be main's tip), or investigate explicitly. No fallback accepts an unsigned
local receipt or a different candidate.

Two owner-authorized NON-PUBLISHING dispatches measure the actual hosted doors:
append `-f rehearsal_fail=true` to deliberately fail before the gate, and
append `-f delivery_rehearsal=true` to run the whole gate and offline packaging
then deliver an authenticated receipt to the read-only main receiver. The latter
receipt explicitly says `rehearsal: true`; tag creation and signing/publication
require `rehearsal: false`. Missing, null or malformed values refuse. Compare
remote refs and releases before and after, and retain source/finalizer/receiver
run URLs. These rehearsals do NOT authorize a real release.

Publication is resumable, not a transaction across independent services.
Build, sign and upload into a draft, verify the complete asset set and exact-tag
signature, then publish the draft. Never overwrite assets of a public release.
Registry publication and the cask branch come last; an existing version is
success only after checking equivalence, not merely swallowing a conflict.
Retry publication for the same immutable tag/SHA and authenticated receipt.
A finalizer failure has a documented manual publish-only dispatch fallback.
No retry moves a tag or blesses a different commit under an existing version.

Acceptance includes a deliberately failing preflight through the production
entry point, asserting remote refs and releases are unchanged; forged, failed,
wrong-ref, wrong-version and wrong-SHA receipt refusals; and a hosted rehearsal
proving a finalizer's `GITHUB_TOKEN` dispatch really starts the receiving workflow.
Until those measurements pass, this document is the approved design, not a
claim that the new release path has shipped or that a version has been released.
