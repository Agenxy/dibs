## Design

[docs/ARCHITECTURE.md](ARCHITECTURE.md) is the practical version: how the
pieces fit, where validation belongs, what must stay true, and the four bug
classes that keep recurring here. Read it before fixing something: the useful
question is usually not "what does this code do" but "what was it supposed to
do".

The full design is in [SPEC.md](../SPEC.md): v1.1, living: committed rather than
frozen, and hardened by five adversarial external review rounds. The short version: a single-writer event loop over a pure
state machine, command-sourced into an fsync'd hash-chained JSONL ledger; one
monotonic serial totally orders everything; replay is exact
(`state == fold(ledger)`), which makes the whole system deterministically
simulatable: the test suite drives randomized op/time sequences and asserts
replay equivalence.
