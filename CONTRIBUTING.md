# Contributing

Criticism is welcome, including the kind that says the design is wrong. If you
think a decision here is a mistake, open an issue and say so plainly — the
reasoning behind most of them is written down (see `SPEC-*.md` and the comments
at the top of each file), so there is something specific to argue with.

## Getting a change to build

**First, once per clone.** The toolchain — Go, the linter, Task itself, Bun,
and the release tools — is pinned with [mise](https://mise.jdx.dev) so the gate
behaves the same everywhere. mise will not read a config file it has not been
told to trust, so a fresh clone needs:

```bash
mise trust && mise install
```

Skip it and `task ci` fails with `No version is set for shim: task`, which reads
like a broken install rather than an untrusted config file.

Then the gate itself:

```bash
task ci
```

It takes a few minutes and needs two things beyond Go:

- **Chromium**, downloaded on first run by `bunx playwright install` — the panel
  and web-board suites drive a real browser, because a DOM shim has no layout and
  the size assertions it made were vacuous.
- **Xcode command line tools** (macOS), for the Swift presence helper. Without
  them `task install` skips it and Touch ID falls back to the admin password;
  nothing else is affected.

Working on one area? The gate splits: `task test` (Go only, seconds),
`task test:panel`, `task test:web`, `task test:channel`, `task test:guard`.
Run the whole chain before opening a pull request.

That is the whole gate: vet, lint, `go test -race` in both build
configurations, the browser end-to-end suites, the human-flow suite, the
coverage floor on `core` and `ledger`, a cross-compile matrix, and govulncheck.
It is the same set the pull-request workflow runs, so a green `task ci` locally
should mean a green CI.

`task install` builds and puts `lanes` and `lanesd` in `~/.local/bin`.

### Working on the human actions, without a fingerprint

The panel's human actions are gated on Touch ID, which no test can produce and
nobody wants raised on their Mac by a test run. Build a dev daemon and script the
verdict instead:

```bash
go build -tags lanesdev -o bin/lanesd-dev ./cmd/lanesd
```

Run it with `LANES_PRESENCE_MOCK` set to `verified`, `declined`, or
`unavailable`. Use the last two — they are the branches a working sensor hides,
and the panel has to say something different and correct for each.

Two things about that variable are deliberate and worth knowing before you reach
for a shortcut. It does nothing without the build tag, by construction rather
than by convention: the code that reads it is not compiled into a release binary,
and `SECURITY.md` explains why that had to be a compile-time boundary. And when
it *is* in force, every result says so, so a scripted unlock never reads as proof
the real path works.

## What a good patch looks like here

**Say why in the code, not just what.** The comments in this repository explain
the reasoning, the measurement, or the wrong turn that led to the current shape.
A patch that changes behaviour without changing the comment that justified the
old behaviour will be sent back.

**Bring the test that fails first.** Not as ceremony — this codebase has shipped
tests that passed with the signal deleted, coverage that counted operations it
never exercised, and a "fix" for a rendering bug that did not exist. If you have
not watched your test fail for the reason you think it fails, you do not yet
know what it asserts.

**Assume the thing you built is unreachable until something calls it.** The most
repeated defect here is code that is present, correct, and wired to nothing: a
validator nobody invoked, a tool implemented but never declared, a parameter an
agent is told about that no handler reads. There are now tests for some of those
shapes. Adding one for a shape it cannot yet catch is a genuinely valuable patch.

**No shell scripts.** Not for build steps, task running, install, hooks or test
harnesses. Shell is untyped, continues past failures unless every script
remembers `set -euo pipefail`, quotes wrong under whitespace, and cannot be
tested or type-checked — so it is the format things rot in. Reach for the
project's runner (`task`), Go, or Python with a `uv` shebang and PEP 723 inline
dependencies. The fleet scenario used to be 600 lines of bash that shelled out to
`python3` seven times to parse its own JSON, which is what that rot looks like
from the inside.

## Platform

Developed and verified on macOS. The process-inspection layer in
`internal/liveness` shells out to `ps`, and its flags differ between BSD and GNU
userlands, so the parts that read another process's environment and CPU time are
the most likely to need work elsewhere. See the README for what is verified.

Patches that make it correct on Linux are wanted. Please include the evidence —
which command you ran, on which distribution, and what it printed — rather than
only that the tests passed, because most of those tests skip when they cannot
find what they need.

## Security

Do not open a public issue for a vulnerability. `SECURITY.md` has the process.
