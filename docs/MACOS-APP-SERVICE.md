# macOS app identity and protected-folder access

This is the implementation design for the 2026-10-22 operator request. Source,
fixture measurements, installed migration, and pane acceptance are distinct.
No production migration or permission change has been performed for this work.

## Selected layout

Keep `Dibs.app`, bundle ID and notifier main code ID `org.agenxy.dibs`, with
`dibs-notify` as its main executable. Place `dibd` and `dibs-presence` beside it
under `Contents/MacOS`. The daemon's code ID remains distinct from the notifier.
Both helper resolvers must understand the bundle layout; moving the daemon
alone would lose notifications and Touch ID.

Ship `Contents/Library/LaunchAgents/org.agenxy.dibs.daemon.plist`, using
`BundleProgram = Contents/MacOS/dibd` and `AssociatedBundleIdentifiers` naming
the outer app. The app's main executable performs SMAppService registration,
status observation and unregistering. Registration is subject to macOS approval;
`requiresApproval` must be visible in doctor and is not successful daemon start.
Install and upgrade must unregister/re-register changed executable/plist bytes.

The former user LaunchAgent must be unloaded before registering the new one.
Its exact `-dir` is retained, and its file is removed only through the migration
path. Failure must never create a second daemon or start a new empty board.

The archive retains a regular root `dibd` for existing installers and manual
entry points. It replaces itself with the paired app daemon in the same PID,
preserving argv and environment. The bundled location prevents an exec loop.
A present incomplete or stale app is refused with the compared paths and
Mach-O section fingerprints; a development image with no paired app remains
standalone. Homebrew's moved app is considered only for a Caskroom entry point.
The archive guard compares the same sections and reconstructs the full app to
verify its deep/strict seal. Detached verification of a bundle main executable
is invalid because its signature also seals its Info.plist.

## Directory selection

Use one private service configuration at
`~/Library/Application Support/org.agenxy.dibs/service.toml`, mode 0600. It is
outside the signed app and independent of the selected data directory. One
resolver serves daemon, CLI and stdio bridge: explicit `DIBS_DIR`, service
configuration, then existing current/legacy directory preference. The bridge
reads the configuration per request. A malformed configuration refuses loudly;
a service start requiring a pin also refuses a missing pin. Neither falls back
to an empty board. Doctor reports the source that selected the directory.

The same private file may carry `service_path` and `service_path_source`
(`migrated` or `configured`). Migration retains the legacy unit's PATH verbatim,
including an explicitly empty value. Only `dibd -app-service` applies it; CLI
and bridge lookups never change their environment. With no path field the
service uses `/usr/bin:/bin:/usr/sbin:/sbin`. Doctor names the value and source.
Unsupported legacy flags or environment keys must be named and refused before
the old service is stopped, with a corrective step.

`dibs configure --service` explicitly authorizes registration. On an existing
service it uses upgrade's replay preflight and fleet confirmation. The private
pin is written before registration; the old plist remains until the new board
answers. Failed registration is unregistered before restoring the old pin and
supervisor. Uncertain removal refuses a second supervisor. A private `.migrated`
receipt retains the old plist bytes. Unsupported custom stdout/stderr paths,
restart policies, flags and environment variables are refused, never dropped.
Directory renaming is refused during app migration; the existing directory is
retained. No installed service is created merely by upgrading a manual daemon.

Source installation and cask postflight invoke the explicit service command.
`dibs configure --service --remove` unregisters the service and retains board
data and the directory pin. The current Homebrew flight-block adapter discards
upgrade/reinstall context before invoking an uninstall hook, so an unconditional
unregister hook would stop the old service before replay preflight. The cask therefore prints an explicit pre-uninstall removal command. If the
app was already removed, restore it through the same install channel, run the
removal command, then uninstall again. Restoration briefly enables the service
and can request folder reads. The leftover background row can be switched off
in Settings meanwhile. Doctor reads bounded per-UID background-item records,
ignores disabled history, and checks for the missing canonical app program; it
calls a record inert only when no job is loaded. It never resets background
items or changes consent. A provably current registration is a no-op on repeat
postflight, without another folder-read request.

A running bridge re-reads the directory pin per attempt. Its destination, trust
roots and credential come from one selected directory; refreshing only the
credential can disclose the new board's secret to the old endpoint. Address
discovery remains cached until a directory change or a refused dial.

## Measurements on macOS 27.0, build 26A428, Darwin 27.0.0

Three isolated apps with unique IDs were built: secondary daemon, nested daemon
app, and daemon as the main executable. Each passed `codesign --verify --deep
--strict` and reported `Bundle.main` name Dibs. Those are metadata and signature
measurements, not evidence of a pane label.

| Observation | Result and limit |
| --- | --- |
| Secondary daemon, real launchd folder read | Desktop/Documents reads completed; TCC's unified log selected the outer unique bundle ID as its authorization subject. No permission prompt was answered by the agent. |
| Daemon-main folder read | Remained pending until the fixture job was stopped. No access or pane-name claim follows. |
| Manual legacy bootstrap | Fixture jobs ran, but the BTM dump had no corresponding records. This fixture did not prove Login Items attribution. |
| SMAppService, ad-hoc signed secondary layout | Register returned enabled; launchctl showed a running ServiceManagement-owned job with the outer parent bundle and a BTM UUID. BTM named the parent app Dibs. |
| Login Items after that registration | Accessibility and a screenshot both showed Dibs with the actual three-dot icon and Running in background. The legacy production dibd row remained alongside it. This name and icon are measured. |
| SMAppService, local certificate | The same layout signed with the existing Dibs Local Codesign identity passed strict/deep verification and registration returned enabled. |
| Files & Folders, Firewall, Notifications naming | Still open. No attributable fixture Files & Folders row was established; bundle metadata alone cannot close these pane checks. |

The isolated `org.agenxy.dibs.layout273257.*` jobs were unregistered or booted
out, all their user/temp LaunchAgent plists removed, and their apps unregistered
from LaunchServices. Each job was verified absent. Login Items retained a Dibs
historical row reading Last ran in background today. It identifies the unique
secondary fixture, not a running service. No Settings entry was toggled/deleted;
the operator may tidy that history. Existing production dibd rows were untouched.

## Access observations and acquisition

A real bounded directory read is the access measurement and may itself request
OS consent. Metadata checks cannot prove a grant. Do not inspect TCC databases
or use private TCC APIs. Keep at most one in-flight read per protected root; a
kernel call that outlives the bound is pending, not denied. Startup/registration
probe only roots containing local agents' directories. Explicit install primes
all three roots; human-present doctor/upgrade can request a fresh attempt where
access is not granted. Never add a periodic retry or claim a dialog appeared or
permission was granted merely because an attempt was requested.

Installation waits only for bounded board readiness, verifies the serving node
belongs to this machine, and posts one fixed-root request to the board selected
by the service's directory/configuration. A foreign `DIBS_ADDR` in the invoking
shell cannot redirect that acquisition or its credential. JSON doctor is read
only unless `--request-folder-access` is explicit. There is no permission-result
poll or automatic retry of a pending read.

The daemon publishes the observed per-root state and corrective System Settings
path. Source/release signing identities remain separate; switching channel can
require a fresh grant. Moving attribution to the final app may also need one
fresh grant, which must be announced before migration.

Apple references: [code signing requirements](https://developer.apple.com/documentation/technotes/tn3127-inside-code-signing-requirements),
[SMAppService](https://developer.apple.com/documentation/servicemanagement/smappservice),
[Files & Folders](https://support.apple.com/guide/mac-help/mchld5a35146/mac).
