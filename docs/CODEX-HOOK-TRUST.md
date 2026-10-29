# Codex hook approval and plugin identity

Codex approves plugin content, not a Dibs release. Dibs must preserve an approval
when the approved bytes are unchanged and must disclose the need for a new
review when they change. It never copies approval to changed content.

## Measured contract

On 2026-10-29, the bundled ChatGPT CLI at
`codex-cli/CodexCLI.app/Contents/MacOS/codex` reported
`0.165.0-alpha.1.2`. A private HOME/CODEX_HOME fixture used the actual plugin
installer and supported app-server trust operation. Changing only the old Dibs
build stamp left all three hook keys and declaration-hash halves unchanged;
the shared bundle-content hash changed and each trusted hook became modified.
This measured metadata approval, not hook execution or mail delivery. No app
thread or agent turn was started.

The inspected upstream revision is
[`4bc746c94fce95ec7ca5f1a91a786156d240c8a2`](https://github.com/openai/codex/tree/4bc746c94fce95ec7ca5f1a91a786156d240c8a2):

- [Plugin keys](https://github.com/openai/codex/blob/4bc746c94fce95ec7ca5f1a91a786156d240c8a2/codex-rs/hooks/src/declarations.rs#L35)
  use plugin ID and relative source path, followed by event/group/handler indices.
- [Declaration hashing](https://github.com/openai/codex/blob/4bc746c94fce95ec7ca5f1a91a786156d240c8a2/codex-rs/hooks/src/engine/discovery.rs#L981)
  uses the event and normalized matcher/handler declaration.
- [The listing identity](https://github.com/openai/codex/blob/4bc746c94fce95ec7ca5f1a91a786156d240c8a2/codex-rs/hooks/src/engine/discovery.rs#L838)
  combines that declaration hash with the plugin content hash.
- [Bundle fingerprinting](https://github.com/openai/codex/blob/4bc746c94fce95ec7ca5f1a91a786156d240c8a2/codex-rs/hooks/src/engine/plugin_fingerprint.rs#L15)
  includes relative names, entry types, sizes and file bytes, with explicit
  exclusions for generated Python/runtime artifacts.
- [Execution verification](https://github.com/openai/codex/blob/4bc746c94fce95ec7ca5f1a91a786156d240c8a2/codex-rs/hooks/src/engine/plugin_trust.rs#L28)
  compares freshly measured content with approval; changed content is refused.

Consequently, changing a versioned root alone does not explain the finding.
The old installer changed `plugin.json` and `.dibs-tools.json` on each Dibs
build. Fixing only the hook body or command cannot undo those content changes.

## Dibs content identity

The installer derives its version from the actual plugin files and current
tools fingerprint. Only generated manifest version and stamp fields are
normalized away before computing the digest, avoiding self-reference. The
emitted metadata contains the bundle digest and tools fingerprint, never the
Dibs build/commit. Identical inputs produce identical files and version roots.
That Dibs digest identifies the normalized payload inputs; it is not Codex's
approval hash of the fully emitted bundle. Approval hashes are always read
from Codex, never reconstructed or copied by Dibs.
Tool, hook, skill, README, server-command or other payload changes remain real
content changes. An existing stable executable entry is preferred only when
file identity proves it names this binary; no launcher is created.

Doctor compares the expected content-derived root and verified files, including
the tools stamp. Bridge schema-change tracking retains its own tools fingerprint;
it does not depend on a plugin build stamp. An unchanged verified plugin is not
reinstalled. Source-choice conflicts and failed-install rollback remain intact.

Migration from an older build-stamped bundle changes the bytes once. That and
later genuine content changes require a person's review through
`dibs codex-hooks --trust` or Codex `/hooks`. Verification of installed files is
reported separately from hook approval and from actual delivery. An unverified
approval probe is reported as unknown, never as trusted.
