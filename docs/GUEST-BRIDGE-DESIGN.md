# Endpoint-scoped guest stdio bridge

Status: accepted in independent Dibs review 16380, with refinements below. Operator choice
recorded in Dibs answer 15770 on 2026-10-03; follows request 14136 and the
measurements in `docs/GUEST-NATIVE-TRUST.md`. This document is not a runnable
recipe, verified adapter, release announcement, cloud acceptance or WAN proof.

## Decision and boundary

A guest's harness launches the **guest's installed `dibs` binary**, not a daemon
on the board host. The binary translates stdio MCP into invitation-authenticated
HTTPS to one literal IPv6 endpoint, using only that endpoint's guest CA. The
harness's general HTTP clients receive no CA, environment trust setting or pin.
The bridge runs no model, starts/resumes no agent, and changes no harness session.

The existing local `dibs mcp-stdio` path remains unchanged. Guest access never
reads a board's local secret, fleet trust, local endpoint discovery, Supgang
profiles, admin settings or wake configuration. Runtime needs only the guest
binary, the private invitation file and network reachability to the invited IP.
Release download is a provisioning convenience, not a coordination dependency:
an operator can deliver the same verified artifact offline from their own host.

## Proposed CLI and private material

Use `dibs mcp-stdio --guest <absolute-invitation-json-path>`. This option selects
a separate typed guest transport before local preflight. It cannot combine with
`--remote-session`, a local-board selector or local credential flags. No guest
bearer, CA text or pin is accepted in argv. Unknown flags, missing values and
conflicting modes fail with an actionable hint. Help contains no secrets.

The bounded, versioned JSON file contains:

```json
{
  "schema_version": 1,
  "name": "<exact invitation name>",
  "endpoint": "https://[<literal IPv6>]:<port>/mcp",
  "invitation_key": "<private bearer credential>",
  "recovery_nonce": "<issuer-minted private 256-bit recovery credential>",
  "expires_at": "<issuer-provided UTC expiry>",
  "ca_pem": "<one guest CA certificate>",
  "ca_spki_sha256": "<64 lowercase hex digits>",
  "bridge_release": {
    "tag": "<explicit published tag containing this mode>",
    "assets": [{
      "goos": "linux",
      "goarch": "amd64",
      "url": "<immutable release archive URL>",
      "archive_sha256": "<64 lowercase hex digits>",
      "dibs_sha256": "<64 lowercase hex digits>"
    }]
  }
}
```

The placeholders are a shape, not valid issued material. Admit at most 64 KiB, one
document, known fields, one CA PEM block, no trailing bytes, no duplicate keys,
and bounded strings. Field names use the schema's canonical lowercase spelling;
case-insensitive aliases are refused too. Use private directories (0700) and regular files (0600),
reject symlinks/non-regular input and unsafe ownership/permissions, and use
atomic exclusive creation for provisioning. Never overwrite an existing recipe
silently. Expiry is advisory locally and authoritative at the server.

Issuer CLI checkpoint: `dibs invite <name> --out <absolute-private-file>` exports
the same typed schema used by the guest reader, including exact issuer expiry
and the derived recovery nonce. This first export is explicitly INCOMPLETE and
not provisionable: it contains no invented `bridge_release`, runnable MCP entry,
download steps or accepted-runtime claim. A release-backed recipe remains owed.
The destination flag is mint-only and is not sent to MCP. The CLI sets
`export: true` on that mint request, through MCP or the private admin API;
only this explicit export response includes `recovery_nonce`. Ordinary mint,
including `export: false`, omits it. Export on list/revoke is refused. This
minimizes disclosure of a credential stable across reissues into transcripts;
the nonce alone still grants no access without a live invitation bearer.
It does not override an omitted `--ttl` or the issuer's policy. Existing
invitation display/list/revoke remain.
The returned identity must exactly match the requested name; a valid but
different mailbox is refused before file publication, with the unused-invitation
list/revoke hint rather than an implicit alias or identity substitution.

Before minting, open an existing owned private directory and reject an existing
destination (including symlinks). Hold that directory handle through issuance,
recheck its privacy, write and sync a new 0600 temporary file, then publish with
an exclusive hard link. A file appearing after preflight cannot be overwritten.
Remove the temporary link and sync the directory before reporting success. No
non-atomic or overwrite fallback is offered when linking is unsupported. A
post-mint failure may leave an unused invitation or an ambiguously durable final
file: preserve it, report the failure and tell the issuer to list/revoke the
unused invitation before retrying. Never print the credential as a fallback.
The board's original at-rest key is part of its state backup, not an arbitrarily
rotatable file; future export help retains this identity-recovery contract.

Endpoint validation precedes networking: HTTPS only; canonical unzoned IPv6,
explicit valid port, exact `/mcp`, no userinfo, query, fragment, encoded alternate
path or DNS name. Production recipes require the same direct-IP policy as the
listener; loopback fixtures exercise the real boundary without adding a shipped
test bypass. IPv4-mapped addresses and arbitrary URL overrides are rejected.

Read the file as one validated snapshot at startup. Every request, retry and
reconnect remains scoped to that snapshot; a failure never reloads local board
configuration or follows a changed invitation file. Address/root/credential
replacement is an explicit new invitation and bridge restart, not implicit
trust rotation. Name and endpoint are public; bearer and recovery nonce are not.

## Exclusive trust, endpoint guard and authentication

Construct a new `x509.CertPool` with exactly the supplied guest CA; never call
`SystemCertPool`, `trustedPool`, `daemonClient`, `refreshLocalSecret` or use the
global default transport in this mode. Parse and validate the CA's validity,
self-signature, CA/key usage, critical exact-IP `/128` constraint, all-DNS
exclusion and pathLen0 shape using the existing guest-root rule, factored to a
shared dependency rather than copied. Verify its SPKI SHA-256 against the
privately conveyed pin before the first connection. A pin compared to the PEM
in the same file proves consistency, not the provenance of that file: the
operator must authenticate this private handoff/out-of-band pin.

Use standard Go certificate verification (`InsecureSkipVerify` stays false),
TLS 1.3 minimum, IP SAN validation against the invited literal IP, then an
additional verified-chain/root pin check. Reject paths containing an
intermediate: this preserves the strict pathLen0 expectation independently of
trust-anchor handling. Preserve normal validity, EKU, signature and name
constraints; never replace them with a pin comparison alone. Root CA and leaf
keys are distinct; pin the CA, not the periodically renewed leaf certificate.

One guarded guest RoundTripper asserts exact scheme/IP/port/path, method POST,
and credential source on **every** call before touching the wire. Disable
environment proxies (`Proxy: nil`); do not perform DNS resolution. Dial only
the parsed literal-IP tuple with a bounded connect/TLS handshake timeout.
Redirects of every status, including same-origin ones, are refused, not followed;
the target gets zero connections and zero credentials. No broad HTTPS client or
public-PKI fallback is exposed. Release downloads use a separate credential-free
client and never receive an invitation header.

Only `Authorization: Bearer <invitation_key>` goes in transport authentication.
No `X-Dibs-Local`, admin proof or `X-Dibs-Agent-Nonce` is sent. An agent token
remains in the agent's tool arguments or MCP `_meta`, distinct from the invite
bearer. Preserve opaque caller tokens, not substitute the invitation for them.
Revocation, expiry, issuer-generation and name binding stay on the real
`publicGate`/invitation paths; the adapter confers no staff role or sub-invite
authority. A TLS failure precedes HTTP, so even a publicly trusted certificate
for the **same invited IP** must get no request/bearer.

## MCP forwarding and startup

Design for 2026-07-28 first: forward each NDJSON JSON-RPC envelope, request ID,
per-request protocol version/clientInfo/clientCapabilities, `_meta`, resultType,
tool content and task handles without rebuilding them as legacy tool results.
Do not negotiate a version or extension for the caller. Translate protocol
transport headers (`Mcp-Method`, version when explicitly stated, and `Mcp-Name`
for tasks/get/update/cancel from taskId) without erasing or contradicting the
per-request metadata. MCP stdio has no HTTP headers; the server already reads
the stateless version from `_meta` (`internal/mcp/mcp.go`, requestEra).

Reuse line framing, synchronized stdout, EOF/parent-exit/signal shutdown,
response validation and outcome classification where policy-independent. Keep
the existing 16 MiB stdio input-line ceiling and document that it is smaller than
the server's 96 MiB request ceiling; do not promise arbitrary 64 MiB inline puts
through this bridge. Bound response bodies and diagnostics too; oversized or
truncated replies yield an unknown-outcome error rather than unbounded buffering.
Separate endpoint resolution, authentication, identity enrichment, upgrade and
retry policies as typed dependencies; do not turn the local bridge into a pile
of guest booleans. Never run the local preflight merely to discover it cannot
find a guest's daemon. Disable local hot-reexec/carry restoration in guest mode;
versioned guest installs are immutable and upgrades require an explicit verified
install and bridge restart by the harness/operator, not an automatic updater.
No placeholder initialize/discovery success: forward the
actual board's capabilities and errors, including older board images.

Preserve `startupGrace = 25s` for initialize/server/discover/tools/list, bounding
the entire connect/handshake/body operation, not just refused dials. The measured
30s default startup limit is specific to the observed installed Codex/Claude
builds, not all harnesses; document explicit startup timeout where supported.
Reuse the ordinary 10s restart grace only for ECONNREFUSED, which proves no
request was accepted; rebuild the body and retry the SAME invited endpoint and
headers. No re-resolution through `boardNow`. ECONNRESET, partial bodies,
timeouts and 5xx imply uncertain mutation outcome and are never replayed
automatically. TLS/auth/redirect/expired-recipe failures are terminal for that
call. Answer an identifiable request with its ID and a guest-specific hint,
stay alive for later requests, and emit no fake response to notifications.
Configuration errors before the protocol starts exit nonzero on stderr.

Invitation connections remain **pull-only**. The production server currently
denies `subscriptions/listen`, `resources/subscribe`, hooks and wake routes
(`internal/mcp/invites.go`, prepareInvitedRequest); the public gate accepts
only POST `/mcp` for coordination. Do not add GET, background subscriptions,
inbox watchers, socket wakes, index shipping or Stop continuations. Forward
subscription refusals honestly; no notification capability may be claimed as
working merely because a private/local listener supports it. Runtime acceptance
must inspect advertised invitation capabilities too; any capability mismatch
needs an explicit server-side guest projection, not invented success in the
bridge. Blob URL transfer is separate existing functionality, not transparently
fetched by this MCP adapter; upload/download URL clients need their own scoped
transport design and must not inherit a general-purpose guest HTTP client.

Legacy initialize/tools/resources request-response remains supported through the
server's real 2025 path, including clientInfo and UI evidence transported from
the actual handshake where necessary. This handshake-only state is explicitly
legacy and never supplies 2026 task capability declarations. There is no
legacy GET/SSE notification promise over an invitation. This is the existing
guest policy, not a reason to weaken it for stdio compatibility.

## Identity, tasks and progress

Do **not** call the local `enrichRegister` wholesale: it inserts pid/session and
host metadata that invited tools refuse. Supply no pid, parent, parent_nonce,
session_id, self-wake flag, app surface, hub host or local pinned-identity
header. Do not mislabel a cloud guest with the board host's cwd. Explicit caller
fields are forwarded to server policy, not silently stripped to fake acceptance;
an illegal harness identity gets the real omit-fields hint. Register must name
the invitation's exact agent name. Guest role/name/kind are the agent's choices
within existing invitation policy, not bridge-created work assignments.

Keep caller-provided nonce and agent token unchanged. The issuer mints a recovery
nonce into the PRIVATE recipe, as sensitive as the invitation key beside it.
Review 17133/17142 refined issuance: derive it with HKDF-SHA256 from the
board's existing at-rest key, no salt, info `dibs guest recovery v1\x00` plus
the invitation name, 32 bytes encoded as hex. Keep derivation inside the
ledger Box and inject only a fixed-name callback into the issuance service;
never disclose or newly store the board key. This preserves the same name's
recovery across invitation, IP and guest-CA reissue without a credential vault.
The key is board state: back it up with the board. Arbitrary replacement is not
supported rotation and breaks both ledger decryption and derived guest recovery.
Any future key migration must carry the original recovery derivation forward
or require explicit identity recovery; reissuing a recipe alone cannot resume
a retained mailbox with a changed nonce. An explicit caller nonce still wins.
A fresh container given the same recipe recovers the same mailbox: its identity
does not depend on a disposable volume. A supplied nonce wins; otherwise use the
recipe nonce. Only for older recipes without one, a guest-private, atomic/locked
credential store may fill one, keyed by endpoint +
CA pin + invitation name, never the board's global `harness-nonces.json`. An
unwritable/corrupt store is an actionable failure for auto-nonce mode, not a new
random identity that silently strands mail. The bridge refuses only that call
before HTTP, preserves its exact request ID, and stays alive for later requests;
notifications receive no synthesized reply. A contended lock's `data.hint`
asks the caller to retry in a few seconds; corrupt/unsafe state asks for private
store restoration or an issuer-exported recovery-nonce recipe. Replies never
include a store path, underlying filesystem error or nonce material. No
automatic retry or local-board remedy is offered. Recipe/startup configuration
errors still exit before the protocol starts. Supplied nonce wins. No token is
printed or automatically substituted into arbitrary tools; registration reply
remains the authoritative credential. File-backed guest identity surviving a
context boundary is distinct from promising persistence in an ephemeral cloud
container. A lost volume with a nonce-less legacy recipe requires explicit
identity recovery, not a sibling registration presented as resumed. Verify the
invitation Bind path reattaches on the same nonce in a new bridge process.
No local daemon/data directory is created.

A guest that first bound its mailbox with a nonce-less recipe's private stored
nonce cannot switch silently to a later recipe's derived nonce: recipe nonce
takes precedence and Bind refuses the mismatch. No production guest used this
unreleased mode. Such a guest must keep its original nonce explicitly (which
wins over the recipe), retain its old recipe, or recover its identity explicitly;
never present a newly minted sibling as recovery.

The legacy store lives beside the private recipe, in endpoint/pin/name-hashed
credential files. All readers/writers take a nonblocking OS lock; contention
is an actionable refusal, not an unlocked write. A new nonce is synced to a
private temporary file then atomically renamed before registration can leave
the bridge. Sync the directory after rename before releasing that nonce, and
repeat the directory sync before reusing existing state so a retry after a
failed sync cannot bypass the durability boundary. Existing corrupt,
non-private or symlinked state is never replaced.

MCP 2026 task support is preserved only when this request declares the extension:
send(track:true) can return CreateTaskResult; tasks/get/update/cancel and opaque
handles retain the server's semantics, invitation/agent-token scope and hints.
Guests poll tasks/get/read_mail; task statusMessage contains progress. The
adapter schedules no polls and converts no task into notifications/progress.
Legacy track:true retains the server's ordinary result and explicit no-task
hint, never a task fabricated from a previous handshake. Tasks cancellation
remains advisory and does not stop another agent. Existing server-side bounded
task retention and durable board replay are not replaced by a bridge cache.

## Artifact recipe and no-sudo provisioning

One issuance payload must carry exact endpoint/name/expiry/key, guest CA/pin,
and a **published supporting version**, per-platform immutable release URLs
and SHA-256 digests. No `latest` URL, guessed checksum, local commit version or
unreleased snapshot is offered as a ready-to-run recipe. Release pipeline owns
the asset manifest (same signed checksums/tag/artifacts); derive names from
`internal/selfupdate` and targets from the release build declarations. Current
artifacts are tar.gz for Linux amd64/arm64 and macOS arm64; Windows/macOS Intel
must be reported unsupported by release provisioning, not mapped to the wrong
binary. An implementation merged on main is not a published bridge binary.

The first-time recipe states these steps literally for the cloud agent using its
available download/hash/archive primitives. No installer program, Python, uv or
cosign is presumed to exist in the guest; no curl-pipe-to-shell. The guest's root
of trust is already the human's private handoff (the same channel carries the CA
pin), so a hash in that handoff has exactly the provenance of the pin. A later
verified-upgrade subcommand may be implemented in Go, not a script, under its
separately reviewed lifecycle boundary:

1. Verify release checksums/signature on the issuer using the existing pinned
   workflow identity and signature boundary; freeze artifact digest records.
   If unavailable, refuse a downloadable recipe rather than silently downgrade.
2. Deliver the private recipe and expected artifact hashes over the authenticated
   invitation handoff. The guest compares downloaded bytes to this independently
   conveyed archive digest, not to a second online checksum it just fetched.
   This is issuer-vouched artifact integrity; it is not a claim that a guest
   without cosign independently verified the release signature. Direct signature
   verification remains available; the existing upgrade verifier is not weakened.
3. Download into an exclusive temporary directory with byte/deadline limits and
   no bearer credential, verify archive SHA-256 before extracting, then extract
   only regular `dibs`, LICENSE and NOTICE entries with safe relative names and
   no traversal, link, duplicate-member or decompression-bomb acceptance. Verify
   the extracted executable digest too; do not install/run dibd, presence helpers
   or notification services in a guest. An operator-provided offline artifact
   enters the same hash/extraction checks.
4. Atomically install into a guest-writable, versioned directory, e.g.
   `<user-home>/.local/lib/dibs/guest/<version>/<platform>/`, never a system path,
   board install or implicit PATH replacement. Keep prior verified binaries
   recoverable. Write the private invitation and credential-store directory
   separately; use an absolute executable/config path in the MCP entry.
5. Print mergeable stdio MCP config (`command`, `args`, and supported startup
   timeout), not overwrite a harness config, install a hook or start a session.
   Claude receives no NODE_EXTRA_CA_CERTS; Codex receives no CODEX_CA_CERTIFICATE
   on this path. Network allowlists separately cover the release download, if
   used, and the invited IPv6 HTTPS tuple for runtime. Container IPv6, network
   policy and a read-only/noexec filesystem can each prevent use; say which one.

Recipe generation must use bounded cached/operator-supplied **verified public**
artifact metadata, not do GitHub I/O inside core, writer loop or a successful
mint's secret disclosure step. A changed runtime feature's minimum version and
release metadata are checked before offering the bridge recipe. No valid
metadata means no runnable adapter recipe; invite/list/revoke behavior and
private fleet access remain intact. No protocol-2026 feature is designed around
long-lived initialize/session state. Artifact acquisition is a separate public
trust context from endpoint TLS; never share pools or credential transports.

## Acceptance, regressions and rollout

All behavioral probes enter the shipped command plus real guestTLS/publicGate,
not a manually constructed transport that skips mode selection. Assert setup
success and watch each added guard fail against the pre-fix implementation or
a controlled mutation of the actual path. Preserve the native strict EXIT1
receipt and unsupported Claude native-extra-CA verdict; a passing adapter is a
different transport, not retroactive repair of the measurement.

- Positive: literal-IP guest CA, leaf renewal, real discover/initialize/tools,
  register exact name + nonce, reconnect same identity, mail, tasks and scoped
  task rejection. Protocol tests cover modern no-initialize and legacy paths.
- Exclusive trust: install a separate CA in a controlled system/public root
  pool and serve its valid same-IP leaf; guest command must refuse with HTTP
  count zero. A control client using that pool must accept it, proving setup.
  This simulates a publicly trusted certificate without claiming to obtain a
  public CA certificate for a loopback fixture. Repeat on supported platform
  builds; also poison extra-CA/default proxy/local-secret settings and assert
  they cannot affect guest trust, destination or auth.
- Wrong CA/SPKI, expired/not-yet-valid certs, EKU mismatch, wrong/mixed IP SANs,
  DNS-only/mixed-DNS, intermediate pathLen bypass, root constraint weakening,
  pin/CA/file mismatch and unknown critical extensions: reject before HTTP.
- Origin guard: DNS/userinfo/query/path/port/IP changes, redirects to same and
  other origins, malicious proxy and private/human/wake routes get no redirected
  bearer; no system trust imports or local secret reads/writes. Verify with
  recipient counters and distinct sentinel credentials, not source-shape alone.
- Real invite expiry/revoke/issuer-close/name/token/task authorization remains
  enforced. Subscription/hook requests are honestly refused without background
  requests. No child process launches an agent; no guest metadata claims local
  session/host authority. Nonce store failure does not create a fresh sibling.
- Startup listener absent then present, accepted-but-stalled TLS/body, permanent
  outage, reset after committed mutation, partial/HTML/empty 4xx/5xx replies,
  notifications and next-call recovery: bounded timings and correct uncertainty,
  no duplicate send or fake initialized/tools success. EOF/parent death/signal
  exits even with inherited pipes; stdout is exclusively valid MCP JSON.
- Provisioner: bad signatures/hashes, missing supporting release, wrong target,
  truncated archive, symlink/traversal/duplicate/oversized member, unsafe paths,
  interrupted install and existing user config/binary preservation.

Sequence: independent design acceptance; failing real-command tests;
implementation and invitation-scoped capability projection; focused races and
full task ci; independent source review; exact hosted gate; normal PR merge;
release the supporting signed artifacts under the operator's release decision;
actual installed guest binaries plus real harness MCP startup; then a distinct
off-LAN/cloud reachability acceptance. Do not conflate these stages or enable
the live listener as a side effect. Supgang address/pin discovery remains its
separate owner/API contract, not a dependency this adapter secretly supplies.

The optional native Codex route may be documented for the measured build with
the accepted additive trust promise (public PKI + IP-scoped guest CA, CA pin
authenticated out of band). It is not exclusive CA/SPKI pinning, does not make
Claude native trust usable, and needs its own exact-build/WAN acceptance. The
stdio route is the exclusive-trust default once its own evidence exists.

## Independent verdict and implementation boundary

Review 16380 accepted the private-file CLI, immutable-snapshot/restart boundary,
issuer-vouched hashes (guest cosign is not mandatory), and unchanged invitation
pull/task-polling policy. Its two refinements are an issuer-minted recipe recovery
nonce and literal first-time download/hash/extract steps instead of a provisioner
bootstrap dependency. Proceed with failing real-command tests first; source
review, gates, publication and installed/WAN acceptance remain separate work.
