## Platform

**macOS is what this is verified on.** Every test, every end-to-end suite and the
CI gate run there, and that is the honest extent of the claim for v0.

**Apple silicon only on the Mac.** Apple is ending Intel support, so the
released Mac archive and the Homebrew cask are arm64: an Intel Mac has to build
from source, which works, and `go install` covers the two Go binaries. Linux
ships both amd64 and arm64.

It compiles for Linux and arm64 on every push, and since September 2026 the
whole suite also RUNS on an ubuntu runner in CI, under the race detector: that
is the answer to "does it work on Linux", and the first run found two things
(a repository-identity cache that trusted inode numbers ext4 reuses, and a
`ps` column that rounds processor time to whole seconds), both fixed. Most of
Dibs is ordinary portable Go with no reason to care. The part that does is
`internal/liveness`, which works out whether a spawned agent is still working
by inspecting other processes: on Linux it reads `/proc`, on macOS it shells
out to `ps` with BSD spellings. What the Linux job does not cover is the
desktop half: `notify-send` is the Linux notifier and it is exercised against
a stub in CI, not a notification daemon. Coordination (agents, claims, mail,
the board) depends on none of that.

Windows builds and vets, and the pure packages (the state machine, the
ledger, the board config, the scorer and the liveness parsers) run on a
windows runner in CI. The first run there said the state machine did not
hold: claim paths were cleaned with the platform's separator, so two agents
were granted the same file. Claims are now folded with `/` whatever the host,
which is what replaying a ledger on another machine needs anyway. The
daemon's lock is `LockFileEx`, liveness asks the kernel whether a pid still
runs, and the rest of the tree (the daemon, the CLI, the e2e suites) has not
been run there; no Windows harness has registered an agent. It is a build,
not a support statement.

Patches for either are wanted, and [CONTRIBUTING.md](../CONTRIBUTING.md) says what
evidence they need.
