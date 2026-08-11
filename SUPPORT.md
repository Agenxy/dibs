# Getting help

**Start with `dibs doctor`.** It exists for this: it names what is broken and
the fix, not just the fault, and it redirects to a file you can paste anywhere.
Stale harness secrets, matching that was never turned on, a data directory a
second daemon is fighting over: it says all of those in one line.

Then, depending on what you have:

| You have | Go to |
|---|---|
| "How do I…", "should Dibs…", thinking out loud | [Discussions](https://github.com/agenxy/dibs/discussions) |
| Something behaved differently from what it said it would | [Open a bug](https://github.com/agenxy/dibs/issues/new?template=bug_report.yml) |
| Something Dibs should do and doesn't | [Open a feature request](https://github.com/agenxy/dibs/issues/new?template=feature_request.yml) |
| A security vulnerability | **Not a public issue**: see [SECURITY.md](SECURITY.md) |
| You want to change the code | [CONTRIBUTING.md](CONTRIBUTING.md) |

## Reading in the right order

- [docs/TUTORIAL.md](docs/TUTORIAL.md): fifteen minutes, from install to two
  dibs catching each other duplicating work. Start here if nothing is running yet
- [README](README.md), what Dibs is, and how to install and run it
- [PHILOSOPHY.md](PHILOSOPHY.md), why it reports and never acts, which explains
  most of the design decisions people ask about
- [SPEC.md](SPEC.md): the design (v1.1, living): single-writer loop, pure state
  machine, hash-chained ledger
- [SPEC-CHANNELS.md](SPEC-CHANNELS.md): duplicate-work matching, thresholds, and
  the rule that a low score never proves two agents will not collide
- [SPEC-SUPERVISION.md](SPEC-SUPERVISION.md): detecting a stalled subagent, with
  the measurements behind it

## What to expect

This is a small project. Issues get read; they do not always get fixed quickly,
and an issue nobody has replied to yet has not been ignored on purpose. If
something is blocking you, say so: it changes the order things get done in.

There is no commercial support and no SLA.
