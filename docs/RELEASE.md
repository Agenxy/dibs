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
gh workflow run release.yml --ref main -f mode=preflight -f version=<canonical-version> -f sha=<full-current-main-sha> -f full_publication_run=<successful-exact-candidate-rehearsal-run>
```

If the finalizer or a downstream service fails, authenticate the successful
preflight run and retry publication without rebuilding a public release:

```text
gh workflow run release.yml --ref v<canonical-version> -f mode=publish-only -f version=<canonical-version> -f sha=<proven-sha> -f preflight_run=<successful-run-id> -f full_publication_run=<authenticated-rehearsal-run>
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
DIBS_TEST_RELEASE_DISCOVERY_VERSION=0.0.11 go test ./tools/releaseflow -run '^TestReleaseDiscoveryRealAPI$' -v -count=1
```

The live probe is opt-in and ordinary CI skips it. By default it reports either
presence or absence; optional `DIBS_TEST_RELEASE_DISCOVERY_EXISTS=true|false`
and `DIBS_TEST_RELEASE_DISCOVERY_DRAFT=true|false` assert a measured expectation,
not a permanent property of the version. After the operator-approved deletion
of that empty draft on 2026-10-04, v0.0.11 is absent from the real release list.

**Tooling changes do not automatically repair an existing tag's publisher.**
The current tagged workflow checks out main for receipt authentication, then
checks out the tag before running the publisher and cask tools. A repair merged
only to main therefore does not reach those later steps on a tag retry. Keep
this limitation explicit; neither moving the tag nor relaxing exact-tag
signature identity is an automatic recovery option.

v0.0.11 is one such frozen failure: the operator approved deleting its empty
draft, while its tag remains unchanged. It must not be retried with the tagged
publisher, which would create another draft and then fail to discover it.
A future version needs the repaired discovery code
and a separate, real full-publication rehearsal before release approval; the
earlier offline/delivery rehearsals did not exercise GitHub's draft API.

## Closed full-publication rehearsal: target bound, external measurement pending

`release-rehearsal.yml` has exactly one scratch target, bound in reviewed source,
not a repository, URL or signing identity supplied by a dispatch:
`Agenxy/dibs-release-rehearsal`. The operator authorized this public repository
on 2026-10-04. Its public visibility, enabled immutable releases and Actions with
read-default workflow permissions were independently checked through GitHub's
API. A different repository or source refuses before build, signature or release
mutation. The initial 2026-10-04 negative run
[37237878575](https://github.com/Agenxy/dibs-release-rehearsal/actions/runs/37237878575)
confirmed the old get-by-tag discovery failure against an exact empty draft.
The positive run
[37238179799](https://github.com/Agenxy/dibs-release-rehearsal/actions/runs/37238179799)
published immutable payload `403225432` with 11 assets, then failed its dry cask
URL check before producing eligible evidence. That failed attempt is retained,
not reused or deleted. No successful full-publication or downstream acceptance
is claimed; the repair requires a new exact-candidate run and unique tag.

Mirror the exact reviewed candidate commit (without rewriting its SHA) and use
`rehearsal-v<version>-<full-candidate-sha>`. The workflow and tool tree must be
identical to the production candidate; a later main commit, including a docs-only
change, requires a new rehearsal. A separate bootstrap ref can configure the
scratch repository, but cannot supply a production-eligible receipt. Ordinary
preflight cannot mint this proof: the scratch publication runs first, avoiding a
circular preflight prerequisite.

The full rehearsal drives the same draft discovery, create, build, keyless sign,
upload, download/readback and publication code as production. Titles and notes
say `REHEARSAL, not a Dibs release, do not install`. It receives no production
Apple signing, Homebrew or registry credential. Apple signing is ad-hoc only;
the production codesign/notarize identity is NOT exercised or accepted. Neither
the tap push nor the registry write is exercised. A static workflow guard checks
identical shared setup action SHAs and inputs for checkout, mise, cosign and
Syft; cosign/Syft versions match the exact shared mise pins, including the
GoReleaser pin. This guards declared toolchain parity, not Apple/downstream
credential acceptance or bit-reproducible output. After immutable public
verification, another call to the same publisher must succeed through an
explicit read-only operation allow-list. Registry and cask plans validate the
canonical version, bundle/sidecar digest and archive URL/checksum pairs, but
never publish, push, merge, install or claim service acceptance.

GoReleaser generates scratch cask URLs with the scratch repository but the
canonical build tag, which exists only locally. Before checksums and signing,
the scratch-only stage verifies exactly three canonical generated URL/digest
pairs against the actual archives, then binds its dry cask to the closed scratch
repository and unique public payload tag. The signed readback validates those
exact payload URLs and digests. Production cask bytes are copied unchanged;
neither arbitrary URLs nor scratch identities enter production trust.

The separate scratch signature verifier requires its exact repository,
`release-rehearsal.yml` and unique tag with the fixed issuer and reviewed trusted
root. It returns no installed `VerifiedRelease`. Installed production identity
remains unchanged; neither rehearsal namespaces nor configurable identities are
accepted by self-update or guest provisioning.

Production validation/preflight and the separate tag job each re-fetch the
successful run's latest attempt and exactly one successful full-publication job
over public HTTPS. A SECOND immutable evidence release is created only AFTER the
payload is immutable and its real read-only retry succeeded. It holds the bounded
receipt and its keyless Sigstore bundle, signed under the ORIGINAL candidate
workflow/tag identity. Signature verification, not API permission or immutable
storage, is the receipt authority. There is no Actions-artifact proof path. Bindings
include repository, run/attempt, workflow/ref, version, SHA, the whole Git tree,
explicit workflow/tool/build objects, release ID and all asset digests. Every
door has an explicit positive outcome; missing/null flags and negative controls
refuse. The gate also downloads and verifies the current immutable public scratch
bytes. Publication authenticates that proof again from its preflight receipt.

The evidence tag is derived, not supplied:
`rehearsal-proof-v<version>-<sha>-<run>-<attempt>`. The signed receipt must name
that exact tag; an attempt-1 receipt cannot be accepted under attempt 2. An
existing evidence release, including an empty draft, is a collision: refuse,
never reuse or overwrite it. Payload and evidence are separate because adding
post-publication proof assets to an already immutable payload is impossible.

This design requires a PUBLIC scratch repository. API metadata GETs to the exact
HTTPS `api.github.com` origin require the existing job's `GITHUB_TOKEN` for
rate-limit authentication, not a new cross-repository credential. GitHub's
[unauthenticated limit is 60/hour per originating IP](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api);
the job token avoids making hosted acceptance depend on that shared allowance.
The token is never sent to release-asset/CDN URLs or non-API redirects. Receipt,
bundle and payload downloads remain public HTTPS; signatures are verified offline
with the same embedded pin-checked root as installed release evidence. An
authenticated HTTP 200 proves NO signature, identity or successful publication.

Preflight does not add `actions: read` for these public metadata calls. The
[run API](https://docs.github.com/en/rest/actions/workflow-runs#get-a-workflow-run)
and [job API](https://docs.github.com/en/rest/actions/workflow-jobs#list-jobs-for-a-workflow-run-attempt)
allow public reads without that permission. The production job's actual token
access to scratch run/jobs/release metadata still MUST be measured in the first
authorized live run; this is not claimed from a local fixture or a general
statement about installation tokens. The actual public network/Actions/signing
doors remain unmeasured until setup and dispatch are authorized. Missing tokens,
HTTP refusals or missing evidence remain blockers: no anonymous, local-file or
unsigned proof fallback exists. A private scratch repository would require a
different approved credential design and is not supported by this path.

After the final release surfaces are stamped and merged, measure the production
repository's cross-repository job-token reads without entering a real preflight:

```text
gh workflow run release-proof-check.yml --repo Agenxy/dibs --ref main -f version=<canonical-version> -f sha=<full-current-main-sha> -f full_publication_run=<successful-exact-candidate-rehearsal-run>
```

This separate non-publishing job calls the unchanged production
`go run ./tools/releaseflow -phase validate` entry. It uses trusted current-main
source and the same validation tool/action pins and `contents: read` job token.
Neither workflow nor job has write or OIDC permissions; no secrets, build,
preflight receipt, tag or publishing step exists. Its workflow name is
`release proof check`, excluded from the finalizer's exact `release` name filter.
It proves that validation door only, not the whole actual production preflight.
Until its exact-candidate hosted run succeeds, cross-repository access remains
unmeasured. Any later source change, including version stamping or documentation,
requires a new exact-candidate scratch rehearsal and proof check.

The old get-by-tag negative control changes ONLY discovery inside the scratch
factory. Run it first against an absent unique target. It must build and sign
through the same code, then fail specifically because the exact empty draft is
visible through LIST but not get-by-tag, before any upload or publication. It
produces no eligible receipt. The corrected run can then reuse that empty draft.
An auth, ref, version, root, build or signing failure is not the negative proof.

Local canonical-version measurement on 2026-10-04 used GoReleaser 2.17.1 with
`GORELEASER_CURRENT_TAG=v0.0.11` and snapshot version `0.0.11`, first with only
the canonical fixture tag and then with the unique rehearsal tag beside it.
Both snapshot builds and archive-member gates exited zero. All six Go binary
hashes, all 39 archive member contents/non-time metadata and the MCP bundle hash
matched. The cask's version/URLs/paths matched, but its three archive checksum
lines did not: binary mtimes changed the archive wrapper hashes. This is exact
canonical build-version evidence, not bit-reproducible archives, a production
macOS certificate or a live publication measurement. The raw receipt remains
at `/private/tmp/dibs-version-rehearsal.kVAC3u/measurement.md` on the measuring
machine; that path is not evidence on another host.

Acceptance includes a deliberately failing preflight through the production
entry point, asserting remote refs and releases are unchanged; forged, failed,
wrong-ref, wrong-version and wrong-SHA receipt refusals; and a hosted rehearsal
proving a finalizer's `GITHUB_TOKEN` dispatch really starts the receiving workflow.
Until those measurements pass, this document is the approved design, not a
claim that the new release path has shipped or that a version has been released.
