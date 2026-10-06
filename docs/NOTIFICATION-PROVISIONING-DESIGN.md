# Proposed notification identity and Time Sensitive provisioning

This is a follow-up decision for the person, requested by the architect (#39924).
Time Sensitive provisioning is not implemented in v0.0.12. That release reports
actual settings and does not claim Time Sensitive capability. Local and published
helper signing identities differ; the install measurement below records the
result rather than promising continuity between those routes.

## What was measured

Before the published install on 2026-10-05, the installed helper's CFBundleIdentifier and designated requirement
identifier are `org.agenxy.dibs`. It is signed by Dibs Local Codesign with no team
identifier or reported entitlements. The one authorized same-identity temporary
probe verified strict signatures and exact designated-requirement equality. It
read authorization authorized, alert style banner, alerts/Notification Center/
lock screen enabled, Time Sensitive not-supported, and Focus not-determined /
not observable. The probe read took 0.475s with 37,060,608 bytes peak RSS; its temporary
bundle/cache was removed after 10.052s and the installed executable was unchanged.
No permission request, posting, settings mutation or install occurred.

The app builder's plist uses `org.agenxy.dibs`, while the release signing tool
overrides the code identifier to `org.agenxy.dibs.notify`. Neither tool supplies
an entitlement plist or a matching provisioning profile. Setting a notification's
requested interruption level to timeSensitive therefore proves no capability.
It must remain separate from the OS's reported timeSensitiveSetting.

## Published-install regression and immediate recovery (47384)

The published v0.0.12 archive was verified against its exact-tag cosign signature
and digest, and its helper bytes matched the installed helper. Its bundle ID is
`org.agenxy.dibs`, but its designated requirement is
`identifier "org.agenxy.dibs.notify" and certificate root = H"27113117309b43e4748ed37d7002d1122a7af9ae"`,
with Authority Dibs Release Codesign and no TeamIdentifier. The previously
authorized local helper had
`identifier "org.agenxy.dibs" and certificate leaf = H"28f1d539c80043baa5e069dfde2d0320d9be4141"`.
Both the code identifier and certificate predicate changed. A stable bundle ID
alone did not preserve this person's authorization across the install routes.

The released helper reported authorization not-determined, alert style none and
alert/Notification Center/lock-screen/Time Sensitive not-supported. The existing
authorization diagnostic returned UNErrorDomain error 1 before scheduling any
notification, with no prompt observed. LaunchServices already recorded the
installed release identity. This was not a measured refusal by the person.

Under architect approval 47441, a temporary copy of the exact released helper
was re-signed with the historical local certificate and identifier only. Strict
signature and exact designated-requirement checks passed; one permission-free
settings read recovered authorized/banner with alerts, Notification Center and
lock-screen enabled. The temporary copy was removed. Applying the same approved
repair to the installed helper, after backing it up and refreshing LaunchServices,
produced the same before/after result. No helper code, accessory activation policy,
LSUIElement, permission or notification settings changed; no notification was
posted. Existing permission was recovered without a foreground authorization flow.
This jointly tests certificate plus identifier restoration; it does not isolate
the contribution of each. It also does not prove a banner was visible.

The CLI and daemon remain exact published release bytes. The repaired helper is
explicitly locally re-signed and is no longer byte-identical to the published
helper. The immutable release archive was not changed. Receipts are retained at
`/tmp/dibs-install-47282/identity-probe.json` and
`/tmp/dibs-install-47282/installed-helper-repair.json` on MacMarine.

## Proposed v0.0.13 repair and acceptance

The release signer must use `org.agenxy.dibs` for Dibs.app, matching its actual
CFBundleIdentifier. Read the actual bundle metadata before signing and refuse
a disagreement; after signing, compare the actual code identifier to that bundle
ID and refuse a mismatch before packaging. Enter the regression through the
release signer and inspect an actually signed temporary bundle on hosted macOS.
The guard must fail on v0.0.12's signer, rather than comparing two constants.
Keep CLI, daemon and presence identifiers unchanged.

After a successful upgrade or fetched-payload install, read the installed helper's
existing permission-free settings mode and report lost notification authorization
with the measured state and the person's corrective path. Include the already-current
upgrade path: daemon version equality does not prove the helper retained its grant.
No diagnostic requests permission, posts, changes settings or re-signs an install.
An old or malformed helper remains unknown. A signature/eligibility refusal must
not be described as the person denying a prompt. A future foreground authorization
flow requires its own native measurement; it is not an established remedy here.

Published archives, Homebrew and fetched installs can share the stable release
certificate and coherent bundle/code identifier. A source build signed with a
different private key cannot promise the same designated requirement. Keeping the
person's local certificate across fetched artifacts, or migrating once to the
release identity with an explicit permission step, is an operator decision; this
emergency repair is not permission to silently re-sign future downloads. Do not
weaken the designated requirement to identifier-only or distribute a private
signing key to manufacture continuity. State this migration boundary in source
install guidance and recheck actual settings after a route transition.

The fetched-payload route also omits the LaunchServices refresh performed by the
documented manual/task install. Refresh the installed bundle as an install step
and report failure honestly. The recorded release refusal occurred with an
existing LaunchServices record, so this omission is not claimed as its cause.
Behavioral install/upgrade fixtures must enter those actual paths, assert their
setup, preserve no-post semantics and fail on the old code. Run the unchanged
full hosted gate and obtain source review before merging the permanent repair.

## One decision, with a migration cost

Choose the canonical helper app identity and approve the matching Time Sensitive
capability/provisioning workflow together. Keeping CFBundleIdentifier
`org.agenxy.dibs` while aligning the release code identifier may reduce one source
of settings churn; choosing `org.agenxy.dibs.notify` changes the bundle identity
as well. Neither choice is a proven transparent migration from the current local
certificate to a Developer ID certificate. Notification grants/settings are
owned by macOS and tied to app identity; a changed identity can require the
person to re-enable permission, Alerts and Time Sensitive. There is no supported
promise that Dibs can copy the person's settings automatically. A person-owned
one-time settings check is an explicit migration cost, not a hidden install step.

After that decision, the account holder/admin enables Time Sensitive Notifications
on the matching explicit portal App ID and generates/downloads its Developer ID
provisioning profile. Verify that the profile authorizes the chosen app ID,
certificate/team, validity and
`com.apple.developer.usernotifications.time-sensitive` before using it. Add a
helper-only entitlements plist with that boolean and embed the matching profile
in the app bundle. Teach appbundle/signrelease to apply it only to this app;
CLI, daemon, presence and unprovisioned local builds do not inherit the restricted
entitlement. Absent/mismatched profiles fail the capability build explicitly or
produce the existing honestly unprovisioned build, according to the approved
release policy; they never produce a falsely claimed capability.

Before distributing the changed identity, verify the signed artifact's actual
identifier, team, designated requirement, embedded profile and entitlements,
strict signature checks and normal launch. Measure getNotificationSettings on
the intended installed artifact. Record whether the existing authorization and
Alerts settings survived, and require the person's settings fix if they did not.
Only an enabled timeSensitiveSetting is positive settings evidence; a requested
level, signed entitlement or accepted post alone still cannot prove visibility.
The native resource/probe window must be coordinated separately; the one-build
measurement exception for #38702 is already consumed.

## Why a profile belongs to the decision

Apple's [macOS supported capabilities](https://developer.apple.com/help/account/reference/supported-capabilities-macos/)
table marks Time Sensitive Notifications as supported for Developer ID signing.
Its [Developer ID certificate guide](https://developer.apple.com/help/account/certificates/create-developer-id-certificates/)
explains that an advanced-capability provisioning profile is evaluated at
installation and each launch. These are capability/profile requirements, not
evidence that this project's current artifact is provisioned. No portal profile
has been obtained or added by this work.
