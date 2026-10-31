## Catching duplicate work

Path claims catch the collision that is cheap to detect: two agents naming the
same directory. Since v1.2 Dibs also catches the one that actually destroys
work: two agents doing the *same job* in different files.

An agent declares what it is doing in its own words. Dibs scores that against
the work already in flight, using the repository's own file layout and **git
co-change history**, and **surfaces the agent already doing it** so they find each
other before the duplicate effort happens:

```
alice: "I am reworking how the session store handles reconnects"
       → OPENED    reworking-how-session-store-handles
bob:   "looking at session persistence when the socket drops"
       → CONSIDER  reworking-how-session-store-handles   score 0.27
                   "read the agent, and join_space if it is the same job"
```

No model, no download, no network. That is tier 0, reading your file names and
your commit history.

**A score proposes; it does not commit you.** The default is `auto_join=declared`:
bob is *shown* alice's agent and decides. Dibs joins an agent automatically only
on a shared identifying ref (`pr:1231`, `issue:88`, a coordination key), because
those name a thing that exists, while a score names a resemblance. Recall at
tier 0 is around 0.3 and precision is not good enough to move somebody's work
without asking; this example used to show bob JOINED, which the shipping default
has never done. `-match-auto-join always` restores unconditional joining if you
want it.

### Tuning it

Matching runs from the first agent registration, one index per repository, and
the daemon measures its own notify threshold on the history it has just mined.
There is nothing to switch on.

What stays off is **joining**. `join_threshold` is 0 until you set it, so Dibs
suggests and never moves anybody:

```sh
dibs calibrate --repo .    # measures YOUR repo, prints two numbers
dibd -match-join <join> &  # notify is measured for you; set it only to disagree
```

`-match-repo` survives as a pre-warm: it indexes a tree before anybody registers
from it, which is worth setting for a daemon started at login and worth nothing
otherwise.

**Stop any daemon already running first.** Dibs refuses to start a second one
on the same machine, and names the one that is running. That is deliberate: two
daemons mean two boards, agents pointed at different ones cannot see each other,
every call still succeeds, and both boards look correct: the exact failure Dibs
exists to prevent, made invisible. If you genuinely want two, say so with
`-allow-parallel` and give each its own `-dir`. Two reasons are good ones:
isolating agents you do not trust (see [SECURITY.md](../SECURITY.md)), and keeping
a client's fleet on a board of its own. Understand what you give up: agents on
separate boards cannot see each other at all, so a shared dependency edited from
both is exactly the collision Dibs would otherwise have caught. One board with
the project shown per agent is the default for that reason.

Better still, put the numbers in `dibs.toml` and skip the flags entirely, which
is what they are for.

**Calibrate first.** Skipping it leaves `join_threshold` at zero, which means
Dibs suggests spaces and never joins one: deliberately, because auto-joining on
a threshold nobody measured is how every agent ends up in a single space. Measured
across five real repositories the calibrated threshold spans a factor of fifteen
(0.022–0.327); there is no default that is not badly wrong somewhere.

Read what calibrate tells you. If it reports that little related work clears the
bar, it is saying matching cannot discriminate on this repository: leave
`join_threshold` at 0 and take the suggestions instead. It says so explicitly
rather than printing a number and hoping.

### How well it actually works

| repository | commits | recall@5 | under a punitive hold-out |
|---|---|---|---|
| hermes-agent | 310 | 0.214 → **0.362** | 0.319 |
| opencode | 2025-11-25; prior wire measurement not repeated | **Changed:** source `501b15af2` on 2026-10-18 (UTC) has a reworked MCP client for SDK v2 and the 2026-07-28 revision (`packages/core/src/mcp/client.ts`, commit `cbe882367`), tracking whether a connection negotiated 2026-07-28 or the legacy `initialize` handshake. Installed `opencode --version` reports literal `local`, not rebuilt from that head, so what an installed session negotiates is unmeasured. Source check and version observation are not a new wire measurement |
| pi-mono | 2025-11-25 in source; installed wire unmeasured | Source `1402973a` on 2026-10-18 (UTC) keeps its own MCP client (`packages/mcp`), whose `LATEST_PROTOCOL_VERSION` is still 2025-11-25. Installed `pi --version` observed 2026-10-18 (UTC) remains **0.84.2**, not rebuilt from that head; no MCP session was measured. Dibs uses [plugins/pi](../plugins/pi) |
| codex | 203 | 0.124 → **0.196** | not measured |

*(before → after the history index, tier 0, no model involved. Reproduce with
`dibs calibrate --repo <path> -n 60 -skip 5`; the punitive column is
`go test ./internal/overlap -run PunitiveHoldout -v`.)*

**Recall@5 near 0.3 is not "solved".** It means that for roughly a third of
declarations the right file is in the top five: enough to put two agents in the
same space often enough to be worth having, and nowhere near enough to trust
blindly. SPEC-CHANNELS §10.1 governs: **a low score is never proof that two
agents will not collide.**

Those numbers are **held out**, and how they were measured is the part to read.
`dibs calibrate` evaluates by using a commit message as the query and that
commit's changed files as the answer, which is the exact pairing the history
index is built from. Measured naively, this change took recall@5 from 0.288 to
0.815 and MRR to a perfect 1.000: the query was retrieving the commit it came
from. Calibrate now holds evaluation commits out of the index, which is also what
production does: index the past, predict the present. The real gain is an order
of magnitude smaller than the leak that hid it.

The obvious next objection is near-duplicates: holding out the exact commit still
leaves reverts, follow-ups and squashed series describing the same work. On these
repositories 51–66% of queries do have another commit sharing two or more
significant terms. So the last column removes those too: every commit sharing
two terms with any query, roughly **half the corpus**: and the gain survives at
+26% to +49% over no history at all. Smaller, and real: near-duplicates
contribute to the improvement rather than being it.
`TestHistoryGainSurvivesAPunitiveHoldout` asserts this rather than describing it.

### Where overlap actually appears in the response

Two mechanisms, two keys. `declare`'s response is the primary integration
surface, so it is worth naming them explicitly:

| Mechanism | Key | Also carries |
|---|---|---|
| **Exact signals**: a shared `refs:` entry, a shared directory | `overlaps` | `warning` |
| **Scorer matching**: related work sharing no literal signal | `spaces` | `spaces_hint` |

They are independent: a declaration can produce either, both, or neither.
`overlaps` needs no index and no threshold, so it works on the first
declaration and across machines. `spaces` needs the repository indexed and a
`notify_threshold` above zero, which the daemon measures for itself, and reports
`matching: "indexing"` while the index is still building.

An operator integrating against Dibs logged `overlaps` and `suggestions`, saw
empty results three times, and concluded matching was broken. It was working
and writing to `spaces`, a key nothing had told them to read.

### Semantic matching

The floor needs no model. To relate work that shares neither words nor history,
point Dibs at an embedding service: one endpoint, so MLX, llama.cpp, Ollama or
a hosted API all satisfy it:

```sh
pip install mlx mlx-embeddings
contrib/embed-sidecar/dibs_embed.py --repo . --port 8737
dibd -match-repo . -match-join 0.33 -match-embed-url http://127.0.0.1:8737
```

An absent or slow sidecar degrades to the built-in scorer and records `degraded`
on any membership it caused: matching gets worse, nothing stops.

**Know the scale limit before you rely on it.** Indexing is one chunk per ~40
lines: this repository is 855 chunks and takes ~110s against a local Ollama. A
7,400-file repository produced **58,710 chunks** and the service gave out partway
through. Dibs fell back to tier 0 and said so, which is honest and is *not*
equivalent, because tier 0 cannot relate work sharing neither words nor file
history. If your repository is large, `-match-repo` can pre-warm a subtree instead of
the whole tree.

### Matching indexes itself

Nothing needs configuring. Every agent registers with a working directory, and
the repository containing it is exactly the history worth mining, so each tree
is indexed the first time an agent turns up in it, up to sixteen of them.

One index per repository, and an agent is scored by the tree it is working in.
A co-change model asked about a different project's sentence does not decline;
it answers confidently and wrongly, which is worse than no matching at all. An
agent in a tree that is not indexed simply gets no semantic suggestions, and
still gets the shared-refs and shared-dirs signals, which are computed in the
core and need no index.

`-match-repo` survives only as a pre-warm, for a daemon started at login that
should have an index ready before the first agent arrives.

Agents working in a different tree are detected as such, and the matcher then
declines to claim evidence rather than inventing it, because the only files two
unrelated projects share are the ones every project has. `dibs doctor` names the
indexed repository and warns when you are working outside it. Indexing several is
[issue #7](https://github.com/agenxy/dibs/issues/7).

### Choosing a model

Retrieval models are asymmetric: a task description and a chunk of code are not
the same kind of text, and every serious one is trained with a marker saying
which side it is being given. Dibs applies the right one automatically, keyed off
the model name. Getting the marker wrong costs more than choosing a smaller model:

| scorer (on this repo)     | recall@5 | MRR   | related work clearing the bar |
|---------------------------|----------|-------|-------------------------------|
| snowflake-arctic-embed2   | 0.551    | 0.807 | **53%**                       |
| built-in (no model)       | 0.284    | 0.542 | 50%                           |
| qwen3-embedding:0.6b      | 0.508    | 0.760 | 49%                           |
| qwen3-embedding:4b        | 0.562    | 0.826 | 42%                           |
| nomic-embed-text          | 0.526    | 0.752 | 36%                           |

The same 4B model *without* its markers scores 22%: half. A four-times-larger
model does not recover a distinction the input never encoded. Getting a marker
*wrong* is worse: arctic-embed scored 42% while being given a document prefix its
card does not specify, and 53% once that was removed.

Which is why Dibs keys per model rather than per family. Families are not
internally consistent, and the differences are invisible from the name:

- **BGE** needs four different things. `bge-large-en-v1.5` wants a trained
  English instruction, `bge-large-zh-v1.5` a different Chinese one, `bge-m3`
  documents that it needs *none*, and `bge-code-v1` / `bge-en-icl` /
  `bge-multilingual-gemma2` want `<instruct>…\n<query>…`.
- **arctic-embed** changed its prefix between v1 and v2. One version apart, same
  vendor, and the two strings share nothing.
- **e5** marks both sides: except `e5-mistral-7b-instruct`, which is
  instruction-style and states plainly that documents need none.

Dibs only claims a convention a model card states. A model it does not recognise
warns and is addressed symmetrically: recoverable, unlike a confident wrong
marker. Measure your own repository rather than trusting this table; that is what
`dibs calibrate` is for.
