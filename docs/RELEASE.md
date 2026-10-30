# Gate before tag, then resumable publication

## macOS signing identity and deliberate rotation

The release certificate root is pinned in
`internal/releasecodesign/identity.go` to SHA-1 requirement fingerprint
`27113117309b43e4748ed37d7002d1122a7af9ae` (Dibs Release Codesign). This
fingerprint is the code-signing requirement's identity, not a content hash.
Changing it can invalidate users' existing macOS privacy and firewall grants.
The signer rejects a different certificate; archive verification checks the
actual designated requirement and validates the extracted executable's signature.
Production preflight and publication require this pin, including refusal of
ad-hoc images. Explicit offline/delivery rehearsals may use ad-hoc signatures;
they provide no acceptance evidence for production identity or grant retention.

Do not rotate the signing secret as routine credential maintenance. A deliberate
rotation requires the owner's approval, a reviewed change to the pin, and a
changelog/migration notice identifying which grants people must review again.
Before dispatch, measure both old-to-new identity and fresh installation with
the final signed app layout. Preserve the old certificate and receipts for
verification of existing releases. Update the signing secret only in that
approved migration window; never widen the check to accept arbitrary roots or
copy the observed new fingerprint into the pin merely to get a green release.
Source installs use their own local signing identity. Switching between a
source install and a Homebrew install is a new identity to macOS and needs one
fresh grant; retaining the release pin does not merge the two channels.

## Homebrew service setup and generated-cask proof

Homebrew 7 is the minimum for the generated declarative `postflight_steps`.
The cask installs files; the person runs `dibs configure --service` once from
an ordinary terminal. Linux then needs `systemctl --user enable --now dibs`.
On macOS, the app's background service may need the person's approval.

This is deliberate: Homebrew's `cask/artifact/abstract_artifact.rb` launches
sandboxed steps with a private temporary `HOME`, and
`cask/artifact/install_steps.rb` does not restore the account home for commands.
A command that registers a per-user service there can target the wrong home.
We do not escape that sandbox or pass account-home access into a flight. Only
Mac-only, non-fatal xattr preparation of the staged files and installed app
remains. The caveats and README retain a manual fallback if that fails.

An existing install updates with `brew upgrade dibs`, then `dibs upgrade`.
Mac app registrations use the stable installed app path. Linux upgrade repairs
only a same-board unit carrying a valid Dibs receipt or matching the frozen
published template, retaining its file mode and moving ExecStart to the stable
package-manager link. A tuned unit needing repair is refused before stopping
the daemon, with an edit instruction. Doctor treats absent service setup and
missing executables as problems and distinguishes automatic repair from local
edits. Manual installs and answering daemons without supervision get a warning.
A successful package upgrade alone is not evidence that the running
daemon was restarted.

`task test:archive` renders the actual cask offline with GoReleaser and then
runs `tools/caskcheck`. Homebrew's own loader and step runner evaluate Mac ARM,
Linux ARM and Linux Intel views; commands are recorded, never run. The guard
requires platform-specific artifacts and caveats, zero deprecation warnings,
no Dibs service commands, two bounded non-fatal Mac quarantine commands and no
Linux flight commands. It does not install a cask, register a native service,
or prove service launch/approval. Check those separately in the public-install
acceptance; never install a Mac cask on a machine whose live service must remain
untouched, even with a throwaway HOME.

## Release flow

A release starts with `workflow_dispatch` of `release.yml` on protected `main`,
with a canonical version and full candidate SHA. The candidate must be main's
current tip and its committed release surfaces must already name that version.
Before preflight can run, the read-only candidate acceptance workflow builds
that exact SHA on macOS with the production GoReleaser snapshot invocation.
An ephemeral Linux runner verifies the SHA/version/digest manifest, installs
the unchanged archives through the generated cask with only its URL authority
adapted, repeats explicit service setup and reinstall, checks frozen v0.0.16
headerless-unit migration through real binaries, and performs MCP round trips.
It receives no release signing identity, tap key or write token. The unsigned
candidate receipt proves installation, not published Sigstore provenance.
Only the measured cask-scoped Homebrew EPIPE signature permits one fresh retry;
the receipt retains the failure and retry. Other failures remain red. Published
v0.0.16 confirmation is separate and retains its known service-setup failure.

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

Before the next release, a main-scoped read-only diagnostic can measure whether
the tap deploy key stored in Actions parses as-is and after newline normalization:

```text
gh workflow run release.yml --ref main -f mode=cask-key-diagnose -f version=<current-version> -f sha=<current-main-sha>
```

It reports only two booleans, not key material or the derived public key, and
does not sign, tag, publish or push. A missing or still-invalid normalized key
is a credential failure, not a reason to use a personal credential. The normal
tagged cask job validates the normalized key before its first tap mutation.
The tap branch still requires a person to merge its PR. v0.0.12 was published
without a cask branch; do not create one, because its notifier has an installed
macOS identity defect. Homebrew users should receive the fixed next release.

## Public install acceptance, after publishing and before announcing

Publishing green does not prove the public install steps work. After publication
and the actual tap-cask merge, run this required check from the repository root
against the exact published tag, with two new receipt directories:

```text
mise exec -- go run ./tools/publicinstall -mode native -tag v<canonical-version> -out /tmp/dibs-public-macos-<canonical-version>
mise exec -- go run ./tools/publicinstall -mode linux -runtime container -fresh-installs 5 -tag v<canonical-version> -out /tmp/dibs-public-linux-<canonical-version>
```

Run `native` on macOS for the Mac receipt. It verifies the published archive's
Sigstore identity and checksum using the production verifier, runs `shasum`,
then exercises archive and exact-tag `go install` binaries in separate private
homes. Each starts a private daemon; two agents register, declare overlapping
work, exchange mail and read the answer over both MCP versions. This measures
coordination through installed binaries, not real model/harness delivery or
native service launch/approval. It never installs a Mac cask or registers a Mac
service, even when the selected HOME is disposable.

The Linux mode uses a new Homebrew container for every first-install attempt,
updates Brew, taps and trusts the public tap, checks that its cask names this
exact tag, installs, configures the per-user unit, reinstalls and repeats setup.
It also runs the archive/Go paths and MCP checks. The container is removed after
each attempt. Image inspection and the exact resolved image digest are retained;
no local replacement cask or candidate archive is substituted. `-runtime docker`
is also supported. The runtime must already work. For Apple container, the CLI
version and configured vminit image must be available; a missing init image
fails with a corrective instead of an implicit fetch. The tool does not start
or update the runtime service. The checker also needs Go and cosign (the Linux
fixture installs these public-path prerequisites), and `shasum`.

When a local runtime is unavailable, the `published Linux install` workflow
uses the same checker on an ephemeral GitHub-hosted Ubuntu runner with its real
Homebrew installation. It runs `-mode linux-hosted -fresh-installs 1`; the receipt
names the hosted run and explicitly distinguishes it from a Mac container.
The current acceptance job pins published `v0.0.16`, runs Brew update before
the public tap/install commands, then performs the same service setup,
reinstall, archive verification, exact-tag Go install and MCP checks. It is a
separate public-release report, not the candidate's required `gate`. Failures
remain red and the receipt plus command logs are uploaded even on failure;
fixture homes, credentials, archives and build caches are not uploaded. One
runner is one fresh attempt, not the five-container failure-rate measurement.
Structured output is decoded from stdout only; stderr remains separately
labelled in each command log. Homebrew `Warning:` lines are also receipt
findings. The published v0.0.16 legacy-postflight warning identifies its source
repair in #519 on main, without implying that release already contains it.

Keep the complete new output directories. `receipt.json` records actual command
exit statuses and separate logs. The Linux parent counts only fresh Dibs install
attempts, reports their failure fraction and EPIPE count, and requires recovery
for every failed attempt. A recognized Homebrew `Broken pipe` can pass only if
the requested cask remains registered, `brew upgrade` succeeds, and the subsequent
reinstall, unit setup and MCP work succeed. Other failures are failures. No
automatic retry or mitigation environment variable hides the measured rate.
Setup failures are outside that denominator and make the check fail. Run the
check once per release decision; a failed result is evidence to investigate,
not a reason to repeat until green. Neither source review nor this tool replaces
the owner's separate service/harness acceptance. Do not announce with a missing
Mac or Linux receipt.

Release preflight receipts are retained for 90 days. An expired or missing receipt is not silently
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
not reused or deleted.

The repaired candidate `6c9d961b24183298a44f67b36731e3eb7c5e07e9` then ran the
[second discovery negative control](https://github.com/Agenxy/dibs-release-rehearsal/actions/runs/37240834531),
which failed at the intended discovery door, followed by the
[successful scratch publication](https://github.com/Agenxy/dibs-release-rehearsal/actions/runs/37241664375).
It produced immutable payload `403239482` with 11 assets and immutable signed
evidence `403244095` with two assets, including a real read-only public retry.
Both OIDC bundles were independently verified against the pinned trust root and
exact scratch workflow identity. The 21 source objects, payload digests, three
guest CLI members, three cask URL/checksum pairs and four Darwin runtime members
across the archive and MCP Bundle were independently checked.

That is pilot proof for **only that candidate**, not for subsequent documentation
or source changes, nor the final v0.0.12 candidate. Production-job-token access
to the public evidence has not yet been measured. Final exact-candidate negative
and positive runs plus production validation are still required; scratch success
does not claim downstream installation, registry or tap acceptance.

From the clean, stamped, exact production-main checkout, prepare the scratch
mirror with the reviewed release tool:

```text
DIBS_RELEASE_TARGET=rehearsal DIBS_RELEASE_VERSION=<canonical-version> DIBS_RELEASE_SHA=<full-current-main-sha> go run ./tools/releaseflow -phase prepare-rehearsal
```

It verifies production main and the absent unique target, refuses a scratch
default branch other than `main`, snapshots every `rehearsal-proof-*` tag and
release, then moves scratch main to the exact candidate with a lease on the
observed old SHA BEFORE pushing `rehearsal-v<version>-<full-candidate-sha>`.
A fast-forward is ordinary; a divergent mirror is explicitly reset and logged
with old and new SHAs. Any concurrent move or changed proof inventory refuses
the unique tag. Scratch main is a mirror, not a release record: prior proof tags
and releases retain their commits and must be unchanged by the move. This
ordering is necessary because GitHub's workflow token
cannot create the later evidence tag when the candidate changes a workflow
file relative to scratch's default branch: `Contents: write` alone receives
HTTP 403 and the token cannot hold `Workflows: write`. The workflow and tool
tree remain identical to the production candidate; a later main commit,
including a docs-only change, requires a new rehearsal. A separate bootstrap
ref can configure the scratch repository, but cannot supply a
production-eligible receipt. Ordinary preflight cannot mint this proof: the
scratch publication runs first, avoiding a circular preflight prerequisite.

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
