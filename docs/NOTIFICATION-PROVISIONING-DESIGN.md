# Proposed notification identity and Time Sensitive provisioning

This is a follow-up decision for the person, requested by the architect39924.
It is not implemented in v0.0.12. That release keeps signing and identity
unchanged, reports actual settings and does not claim Time Sensitive capability.

## What was measured

On2026-10-05 the installed helper's CFBundleIdentifier and designated requirement
identifier are `org.agenxy.dibs`. It is signed by Dibs Local Codesign with no team
identifier or reported entitlements. The one authorized same-identity temporary
probe verified strict signatures and exact designated-requirement equality. It
read authorization authorized, alert style banner, alerts/Notification Center/
lock screen enabled, Time Sensitive not-supported, and Focus not-determined /
not observable. The probe read took0.475s,37,060,608 bytes peak RSS; its temporary
bundle/cache was removed after10.052s and the installed executable was unchanged.
No permission request, posting, settings mutation or install occurred.

The app builder's plist uses `org.agenxy.dibs`, while the release signing tool
overrides the code identifier to `org.agenxy.dibs.notify`. Neither tool supplies
an entitlement plist or a matching provisioning profile. Setting a notification's
requested interruption level to timeSensitive therefore proves no capability.
It must remain separate from the OS's reported timeSensitiveSetting.

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
measurement exception for38702 is already consumed.

## Why a profile belongs to the decision

Apple's [macOS supported capabilities](https://developer.apple.com/help/account/reference/supported-capabilities-macos/)
table marks Time Sensitive Notifications as supported for Developer ID signing.
Its [Developer ID certificate guide](https://developer.apple.com/help/account/certificates/create-developer-id-certificates/)
explains that an advanced-capability provisioning profile is evaluated at
installation and each launch. These are capability/profile requirements, not
evidence that this project's current artifact is provisioned. No portal profile
has been obtained or added by this work.
