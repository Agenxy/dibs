# The pre-release review brief

This is the release surface: everything since the last tag.
Read PHILOSOPHY.md and AGENTS.md first; they are the decision procedure and the
map, and AGENTS.md lists four bug classes that recur in this codebase.

The reason you are being asked: previous releases shipped bugs that a careful
reader would have caught, and each one burned a version. Optimise for finding
those, not for style.

Weight your attention here, hardest first:

1. CORRECTNESS in internal/core and internal/engine. The invariant is
   state == fold(ledger). Specifically:
   - Anything validated in core.Apply that belongs in core.Admit. A rule added
     to Apply is retroactive and makes the daemon refuse its own ledger on boot.
   - An op that changes replayable state without advancing the serial, or the
     reverse. The engine ledgers exactly when the serial advanced.
   - New json tags on core.Op / core.Message: `choices`, `grant`, `adopt`,
     `no_process`. Renaming one is silent data loss. Check they are frozen and
     that nothing reads a field it never sets.
   - Concurrency: the engine is a single writer. Anything touching e.state
     outside the loop, or blocking the loop on a human, is a bug.

2. AUTHORISATION on the newest paths. A request can now carry `grant` (a role)
   or `adopt` (somebody's mailbox), and APPROVING it performs the effect.
   Try to find a way for an agent to promote itself, to have another agent
   approve for it, or to take a mailbox it should not have. Also
   cmd/dibd/guard.go, which now mints a board session against Touch ID.

3. Anything that reports success while doing nothing. This release fixed four
   of those and it is clearly a pattern here: a hook that returns a digest
   nobody reads, a notification posted into a Focus mode, an approval recorded
   with no effect, a repair spelled as a register that short-circuits.

4. Tests that cannot fail. Probes asserting on their own setup, tests pinning a
   string rather than a behaviour, anything that would pass against the code it
   was written to catch.

Report concrete findings with file:line and why it is wrong. Say plainly if a
section looks fine. Do not fix anything; I want the list.
