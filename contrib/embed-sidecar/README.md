# A minimal `/v1/embeddings` server

**You probably do not need this.** Dibs talks to any OpenAI-compatible
embeddings service, and you likely already run one:

```bash
ollama serve                                   # MLX-accelerated on Apple Silicon
ollama pull qwen3-embedding
dibd -match-repo . -match-embed-url http://127.0.0.1:11434 \
       -match-embed-model qwen3-embedding
```

vLLM, text-embeddings-inference, LM Studio, llama.cpp's server and hosted
providers all work the same way. Dibs only ever calls `POST /v1/embeddings`.

This directory exists for one case: **running an MLX model that your serving
stack does not carry**, such as `codefuse-ai/F2LLM-v2-4B`. It is ~200 lines,
serves the standard endpoint, and is a reference implementation rather than a
protocol of ours. `--backend hash --self-test` checks the contract with no model
at all, which is how CI covers it.

Dibs owns the repository index and the similarity maths. This serves vectors
and nothing else.

```bash
python3 -m venv .venv && .venv/bin/pip install mlx mlx-embeddings
.venv/bin/python dibs_embed.py --model codefuse-ai/F2LLM-v2-4B --port 8737
```

## What it is for

Dibs' built-in scorer relates work by shared words and by files that change
together. That covers a great deal and cannot cover everything: two agents can
be doing the same job in files with no shared vocabulary and no shared history.

**Dibs** embeds the repository and uses the declaration to retrieve code, so
what it compares is predicted *file sets*, never two sentences: two tasks
unrelated in English embed as unrelated in English, which is exactly the
collision spaces exist to catch (§0, §4.2).

This process does none of that. It turns text into vectors.

## The contract

```
POST /v1/embeddings  {"model": "...", "input": ["...", "..."]}
                ->   {"object": "list",
                      "data": [{"object": "embedding", "index": 0,
                                "embedding": [0.01, -0.4, ...]}, ...]}
```

That is the OpenAI embeddings API verbatim, which is why any serving stack
works and why nothing here is a Dibs protocol. Order matters: Dibs maps
vectors back to chunks by `index`, so a reordered or short reply is rejected
rather than allowed to misalign the index.

Weights are raw similarities computed by Dibs. **Dibs renormalises them**, so
a service returning distances rather than similarities cannot silently shift a
calibrated threshold.

## Failure is a downgrade, not an outage

If this process is absent, slow, or wrong, Dibs falls back to its built-in
scorer and records `degraded` in the provenance of any agent membership that
resulted. Matching gets worse; nothing stops. A scorer that cannot answer is
**not** evidence that two agents will not collide (§10.1).

## Which model? Measured, not assumed

Every figure below is `dibs calibrate` on the Dibs repository, 60 commits,
identical cases, identical metric. This is a **contamination-proof** benchmark:
no published model has trained on a private repository's history.

Re-measured after the distribution rescale (SPEC-CHANNELS.md), because that
change altered what the numbers *mean*: it rewards separating related from
unrelated work rather than scoring high across the board.

| | tier 0 (no model) | Qwen3-Emb-0.6B | Qwen3-Emb-4B | **F2LLM-v2-4B** |
|---|---|---|---|---|
| recall@5 | 0.284 | 0.392 | 0.472 | **0.529** |
| recall@10 | 0.488 | 0.521 | 0.570 | **0.638** |
| recall@20 | 0.653 | 0.677 | 0.677 | **0.779** |
| MRR | 0.542 | 0.667 | 0.739 | **0.781** |
| calibrated join bar | 0.327 | 0.536 | 0.555 | **0.362** |
| licence | n/a | Apache 2.0 | Apache 2.0 | Apache 2.0 |
| download | 0 | ~1.1 GB | ~8 GB | ~8 GB |

The **join bar** row is the one the rescale added, and it is the most useful
number here. It is the 95th percentile of scores between *unrelated* pairs, so
lower is better: F2LLM's 0.362 against Qwen3-4B's 0.555 means a far wider margin
between "this is the same job" and "this is not". Auto-join depends on that
margin, and recall alone would never have shown it.

**F2LLM-v2-4B wins on every metric**, before and after the rescale: the
ranking survived a change that altered its own reasoning. The rescale helped
Qwen3-4B most (MRR 0.649 → 0.739) and left F2LLM and 0.6B unchanged, which is
what you would expect: it removes confident-but-wrong matches, and a model that
was not making many has nothing to lose.

This result overturned the
expectation going in. On the public MTEB(Code) board F2LLM leads Qwen3 while
having seen 58% of that benchmark's evaluation data in training, so its lead
looked like contamination. On a benchmark it cannot have trained on it still
wins: by *more*, not less. The contamination critique was methodologically
right and the conclusion drawn from it was wrong.

Second finding: **0.6B is most of the way there.** It captures ~70% of the
MRR gain over tier 0 for one seventh the download, and beats the 4B Qwen on
MRR. If disk or memory is tight, 0.6B is not a consolation prize.

Third: **tier 0 is a real floor, not a placeholder**: 0.488 recall@10 with no
model, no download and no network.

### Recompare on your own repository

```bash
dibs calibrate                                   # tier 0
dibs calibrate -embed-url http://127.0.0.1:8737  # whatever the sidecar is serving
```

Restart the sidecar with `--model <name>` between runs. Two caveats:

- **Thresholds are per-model.** The calibrated `join` bar was 0.363 (tier 0),
  0.568 (0.6B), 0.602 (Qwen3-4B), 0.370 (F2LLM). A model scoring higher across
  the board is not better, it is differently scaled. **recalibrate on switch**.
  Compare recall and MRR, never raw scores.
- **Sample size matters.** At n=25 the Qwen3-4B/F2LLM gap looked much narrower
  than at n=60. Use at least 50 commits before trusting an ordering.

## If indexing times out

A bigger model on a busier machine needs longer per batch than a small one, and
the failure lands on the FIRST batch (`embedding chunk 0/449`) which reads
like a broken service rather than a slow one.

Dibs scales the deadline with batch size (a 64-chunk batch gets far longer than
a one-word probe), and says which knob moves it:

```
embeddings service did not answer within 3m30s for a batch of 64,
a larger model on a busy machine needs longer: raise -match-deadline
(daemon) or use a smaller model
```

Timing out mid-index is worse than being slow: a half-built index silently
matches nothing, which looks exactly like a quiet fleet.

## Choosing a model

The default is **Qwen3-Embedding-4B**: Apache 2.0, and as of 2026-07 the
highest-ranked 100%-zero-shot model under 8B on the live MTEB(Code, v1) board.
Models ranking above it are either non-commercial or trained on a large share
of that benchmark's own evaluation data.

Do not take that as settled. Run `dibs calibrate` against your repository: a
commit message is a task declaration and its changed files are the label, so
your own git history is a benchmark no published model has trained on.
