## Platform

The complete CI gate, including the desktop end-to-end suites, runs on macOS.
The native Linux `linux` job runs the race suite and the kernel peer-identity
test. Require `linux` alongside `gate` in the main branch ruleset.

**Apple silicon only on the Mac.** Apple is ending Intel support, so the
released Mac archive and the Homebrew cask are arm64: an Intel Mac has to build
from source, which works, and `go install` covers the two Go binaries. Linux
release archives cover amd64, arm64, and riscv64. Install on RISC-V from the
signed release archive. A guest handoff includes the RISC-V archive and `dibs`
member when both digests are in the signed release checksums. Homebrew casks
cover macOS arm64 and Linux amd64 and arm64.

It cross-compiles for those Linux targets on every push, and since September 2026 the
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

The advisory `linux-riscv64` job runs the state machine, ledger, and board
configuration packages under QEMU user-mode emulation. The native Linux job
provides the race and kernel peer-identity evidence.

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
