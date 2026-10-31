## Engineering

Pinned Go toolchain (mise), golangci-lint v2 at **zero warnings**, `-race`
everywhere, property-based replay tests, GoReleaser with reproducible builds plus
cosign and SBOM. `task ci` runs the full local gate chain and is the same set CI
runs.

The complexity ceilings in `.golangci.yml` carry named, reasoned exclusions
rather than a blanket suppression: dispatch tables (the state machine's one
`Apply` switch, the MCP tool switch, the CLI verb switch) score high because a
dispatch table is nothing but branches, and splitting them would hide the
exhaustiveness a reader needs to check. Every exclusion says which functions and
why; `gocognit` still fires everywhere else, and it caught four functions in the
spaces work that genuinely needed splitting.

End-to-end suites run against a real daemon over real HTTP, and the browser
surfaces against real Chrome: `panel` (89), `web` (101), `space` (106), `guard`
(36). All four are in `task ci`, alongside the sidecar self-test, the human-flow
suite and the alternate `dibdev` build.

The space suite measures its own join bar rather than hardcoding one, because
the scores it asserts on are computed from this repository's git history and
therefore move every time anybody commits. A fixed bar passes until it doesn't,
and then fails for a reason no contributor can act on.

Beyond that, `internal/mcp/e2e/fleet_scenario.py` runs a real fleet. Codex,
opencode and pi sessions with real models, coordinating through Dibs while a
human acts from the board at the same time (37 checks). It is deliberately *not*
in `task ci`: it spends money on model calls and depends on provider
availability. It exists because everything else drives Dibs through its own
client code, and that cannot answer whether a real harness, with a real model
choosing what to call, actually coordinates.

## Contributing

Criticism is welcome, including the kind that says the design is wrong: the
reasoning behind most decisions is written down, so there is something specific
to argue with. [CONTRIBUTING.md](../CONTRIBUTING.md) says what a patch needs here,
[SUPPORT.md](../SUPPORT.md) says where to ask, and
[CODE_OF_CONDUCT.md](../CODE_OF_CONDUCT.md) is the short version of "argue with the
work, not the person".
