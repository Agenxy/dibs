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

## Required operator-owned repository setting

**Enable release immutability is required** in the repository's Settings >
General > Releases. Only the operator owns this setting; the workflow never
changes it or adds an administration token. The operator can verify it in the
settings page or, with their own administration-read credential, inspect:

```text
gh api repos/Agenxy/dibs/immutable-releases
```

The response must report `enabled: true`. It applies only to future releases,
not historical mutable releases. The publication job uses its existing token
to read the release object instead, and requires both `draft: false` and
`immutable: true` after publishing and on public retries. A missing, false,
malformed or unreadable value fails loudly with a repository-setting hint;
no unsigned or mutable fallback exists. Cask publication requires the same
immutable-public postcondition.

Keep the draft check immediately before uploads. If another authorized writer
publishes between that check and the upload, GitHub's immutable-release
enforcement refuses asset mutation. After any upload refusal the publisher
stops mutating and reads the release: success requires an immutable public
release, every required asset verified against the exact-tag signature and
byte-for-byte equal to the local stage, and the exact remote tag object.
Otherwise it fails; it never retries uploads into a public release. The same
read-only proof resolves an ambiguous publish response and rechecks public
bytes after un-drafting, since mutable draft bytes could change before publish.
Global workflow concurrency still serializes cooperating release jobs.

GitHub documents the [immutable-release protections and draft-first workflow](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases)
and [the operator's setting and future-release scope](https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/establish-provenance-and-integrity/prevent-release-changes).

Publication is resumable, not a transaction across independent services.
Build, sign and upload into a draft, verify the complete asset set and exact-tag
signature, then publish the draft. Never overwrite assets of a public release.
Registry publication and the cask branch come last; an existing version is
success only after checking equivalence, not merely swallowing a conflict.
Retry publication for the same immutable tag/SHA and authenticated receipt.
A finalizer failure has a documented manual publish-only dispatch fallback.
No retry moves a tag or blesses a different commit under an existing version.

Release discovery uses the authenticated [list-releases API](https://docs.github.com/en/rest/releases/releases#list-releases),
not get-by-tag, because the latter omits drafts. Scan up to ten pages of 100
releases, match `tag_name` exactly and reject duplicate matches. A full final
page, malformed response or any API failure refuses publication rather than
guessing absence. The publishing credential must have push access to see
drafts. On 2026-10-04, a read-only real API measurement found v0.0.11 draft
402967536 with zero assets while get-by-tag returned HTTP 404. Re-measure the
actual `status()` subprocess door without any release mutation using:

```text
DIBS_TEST_RELEASE_DISCOVERY_VERSION=0.0.11 DIBS_TEST_RELEASE_DISCOVERY_DRAFT=true go test ./tools/releaseflow -run '^TestReleaseDiscoveryRealAPI$' -v -count=1
```

The live probe is opt-in and ordinary CI skips it. Its asserted draft state is
a measurement at that time, not a permanent property of the version.

**Tooling changes do not automatically repair an existing tag's publisher.**
The current tagged workflow checks out main for receipt authentication, then
checks out the tag before running the publisher and cask tools. A repair merged
only to main therefore does not reach those later steps on a tag retry. Keep
this limitation explicit; neither moving the tag nor relaxing exact-tag
signature identity is an automatic recovery option.

v0.0.11 is one such frozen failure: its draft is left untouched and its tag is
not moved. It must not be retried with the tagged publisher, which would miss
the existing draft again. A future version needs the repaired discovery code
and a separate, real full-publication rehearsal before release approval; the
earlier offline/delivery rehearsals did not exercise GitHub's draft API.

Acceptance includes a deliberately failing preflight through the production
entry point, asserting remote refs and releases are unchanged; forged, failed,
wrong-ref, wrong-version and wrong-SHA receipt refusals; and a hosted rehearsal
proving a finalizer's `GITHUB_TOKEN` dispatch really starts the receiving workflow.
Until those measurements pass, this document is the approved design, not a
claim that the new release path has shipped or that a version has been released.
