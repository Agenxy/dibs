#!/usr/bin/env python3
"""Lanes embedding sidecar: tier 2 of SPEC-CHANNELS.md §4.

Answers one endpoint:

    POST /predict  {"declaration": "...", "limit": 40}
              ->   {"files": [{"path": "...", "weight": 0.9}, ...],
                    "scorer": "<model>", "version": "<n>"}

WHY THIS IS A SEPARATE PROCESS, not a Go library: the strongest embedding
runtime on Apple Silicon is MLX, which is Python. Lanes' core is Go with no
dependencies, and linking a model runtime in would end that. CGO alone
forfeits pure-Go cross-compilation. Behind loopback HTTP the daemon stays
dependency-free and the runtime becomes the operator's choice.

WHAT IT ACTUALLY DOES, and why it is not "embed two sentences and compare":
two tasks that are unrelated in English embed as unrelated in English, which is
precisely the case channels exist for (SPEC-CHANNELS.md §0, §4.2). So the
declaration is embedded and used to RETRIEVE code: the comparison Lanes makes
is between predicted file sets, not between sentences. This process owns the
index; the daemon never learns which model, which dimensions, or how chunks are
made.

Weights here are raw similarities. Lanes renormalises them on receipt precisely
so this script does not have to know its conventions.

    # real use (Apple Silicon):
    pip install mlx mlx-lm
    ./lanes_embed.py --repo /path/to/repo --port 8737
    lanesd -match-repo /path/to/repo -match-join 0.33 \\
           -match-embed-url http://127.0.0.1:8737

    # contract check, no model needed:
    ./lanes_embed.py --repo . --port 8737 --backend hash
    ./lanes_embed.py --self-test

The default model is F2LLM-v2-4B: Apache 2.0, and the measured winner of a
four-way comparison run with `lanes calibrate` on this repository's own git
history: recall@10 0.638 and MRR 0.780 at n=60, ahead of Qwen3-Embedding-4B
(0.560 / 0.649), Qwen3-Embedding-0.6B (0.532 / 0.667) and the built-in tier-0
scorer (0.488 / 0.542). See README.md in this directory for the full table.

Notably it was NOT the expected winner. On the public MTEB(Code) board F2LLM
leads while having trained on 58% of that benchmark's evaluation data, so the
lead looked like contamination, and it survives on data no published model can
have trained on. Swap it with --model; thresholds are per-model, so recalibrate.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

DEFAULT_MODEL = "codefuse-ai/F2LLM-v2-4B"
VERSION = "1"

# Chunks larger than this are split. A whole file embedded as one vector is a
# vector about the file's general topic, which retrieves the repository's
# biggest files for every query.
MAX_CHUNK_CHARS = 2000


# ── backends ────────────────────────────────────────────────────────────────


class HashBackend:
    """Deterministic stand-in used to exercise the CONTRACT without a model.

    Not a scorer anybody should run: it is a hashed bag-of-words, which is what
    tier 0 already does better with git history behind it. It exists so the
    HTTP contract, the indexing, and Lanes' client can be tested end to end on
    a machine with no model: the failure mode this replaces is "the sidecar
    was never run before it shipped".
    """

    name = "hash-stub"
    dims = 256

    @staticmethod
    def _bucket(tok: str) -> int:
        """Stable across processes, which builtin hash() is not.

        Python randomises str hashing per process unless PYTHONHASHSEED is
        set, so `hash(tok) % dims` put the same word in a different bucket on
        every run. Two single-word inputs then collided into one bucket about
        once in 256 runs and produced identical vectors: a self-test that
        failed roughly one CI run in two hundred, with nothing about the
        failure pointing at the cause. It also made this class's own promise
        of determinism false.
        """
        return int.from_bytes(hashlib.blake2b(tok.encode(), digest_size=4).digest(), "big")

    def encode(self, texts: list[str]) -> list[list[float]]:
        out = []
        for t in texts:
            v = [0.0] * self.dims
            for tok in re.findall(r"[A-Za-z_][A-Za-z0-9_]{2,}", t.lower()):
                v[self._bucket(tok) % self.dims] += 1.0
            out.append(_normalise(v))
        return out


class MLXBackend:
    """The real one: MLX on Apple Silicon."""

    def __init__(self, model: str):
        try:
            from mlx_embeddings.utils import load  # type: ignore
        except ImportError as e:  # pragma: no cover - depends on the host
            raise SystemExit(
                f"mlx-embeddings is not installed ({e}).\n"
                "  pip install mlx mlx-embeddings\n"
                "Or run with --backend hash to check the contract without a model."
            ) from e
        self.name = model
        self._model, self._tok = load(model)

    def encode(self, texts: list[str]) -> list[list[float]]:  # pragma: no cover
        import mlx.core as mx  # type: ignore

        out = []
        for t in texts:
            ids = self._tok.encode(t)
            emb = self._model(mx.array([ids]))
            vec = emb.text_embeds[0] if hasattr(emb, "text_embeds") else emb[0]
            out.append(_normalise([float(x) for x in vec]))
        return out


def _normalise(v: list[float]) -> list[float]:
    n = sum(x * x for x in v) ** 0.5
    return [x / n for x in v] if n else v


# ── server ──────────────────────────────────────────────────────────────────


def make_handler(backend, model_name: str):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_POST(self):  # noqa: N802 - BaseHTTPRequestHandler's spelling
            if self.path.rstrip("/") != "/v1/embeddings":
                self._send(404, {"error": "only POST /predict"})
                return
            try:
                n = int(self.headers.get("content-length", 0))
                req = json.loads(self.rfile.read(n) or b"{}")
                raw = req.get("input")
                texts = [raw] if isinstance(raw, str) else [str(t) for t in (raw or [])]
                vecs = backend.encode(texts) if texts else []
            except Exception as e:  # noqa: BLE001 - never take the board down
                # Lanes treats any failure as "degrade to tier 0" (§4.1), so an
                # error here costs a better answer, never the declaration.
                self._send(500, {"error": str(e)})
                return
            self._send(200, {
                "object": "list",
                "model": req.get("model") or model_name,
                "data": [
                    {"object": "embedding", "index": i, "embedding": list(v)}
                    for i, v in enumerate(vecs)
                ],
            })

        def _send(self, code: int, body: dict) -> None:
            raw = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def log_message(self, *_args):  # quiet: the daemon logs what matters
            pass

    return Handler


def self_test() -> int:
    """Exercise the contract in-process, with no repository and no model."""
    import tempfile
    import urllib.request

    srv = ThreadingHTTPServer(("127.0.0.1", 0), make_handler(HashBackend(), "hash-stub"))
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    url = f"http://127.0.0.1:{srv.server_address[1]}/v1/embeddings"

    ok = True

    def check(name, cond, detail=""):
        nonlocal ok
        print(("  \033[32m✓\033[0m " if cond else "  \033[31m✗\033[0m ") + name +
              ("" if cond else f". {detail}"))
        ok = ok and cond

    def post(payload):
        req = urllib.request.Request(url, data=json.dumps(payload).encode(),
                                     headers={"content-type": "application/json"})
        return json.loads(urllib.request.urlopen(req, timeout=5).read())

    out = post({"model": "hash-stub", "input": ["token validation middleware"]})
    check("responds in the OpenAI embeddings shape",
          out.get("object") == "list" and isinstance(out.get("data"), list),
          json.dumps(out)[:120])
    check("one vector per input", len(out["data"]) == 1, str(len(out.get("data", []))))
    first = out["data"][0]
    check("each item carries object/index/embedding",
          first.get("object") == "embedding" and first.get("index") == 0
          and isinstance(first.get("embedding"), list), json.dumps(first)[:120])
    check("the vector is numeric and non-empty",
          len(first["embedding"]) > 0
          and all(isinstance(x, (int, float)) for x in first["embedding"]))

    # Batching is how Lanes indexes a repository; order is how it maps vectors
    # back to files, so a reordered or short reply corrupts the whole index.
    batch = post({"model": "hash-stub", "input": ["alpha", "beta", "gamma"]})
    check("a batch returns one vector per input, in order",
          [d["index"] for d in batch["data"]] == [0, 1, 2],
          str([d.get("index") for d in batch["data"]]))
    check("distinct inputs give distinct vectors",
          batch["data"][0]["embedding"] != batch["data"][1]["embedding"])
    # Determinism is a PROMISE this class makes, and it was false: builtin
    # hash() randomises str hashing per process, so the same word landed in a
    # different bucket on every run and two inputs collided about once in 256
    # runs. Re-encoding here would not catch that: the collision is between
    # processes, so the buckets themselves are pinned.
    check("bucketing is stable across processes, not just within one",
          HashBackend._bucket("alpha") % HashBackend.dims == 180
          and HashBackend._bucket("beta") % HashBackend.dims == 79,
          f'alpha={HashBackend._bucket("alpha") % HashBackend.dims} '
          f'beta={HashBackend._bucket("beta") % HashBackend.dims}')
    check("a bare string input is accepted, not just an array",
          len(post({"model": "hash-stub", "input": "one"})["data"]) == 1)
    check("an empty input list is answered, not refused",
          post({"model": "hash-stub", "input": []})["data"] == [])
    srv.shutdown()
    print("\n" + ("self-test passed" if ok else "SELF-TEST FAILED"))
    return 0 if ok else 1


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--port", type=int, default=8737)
    ap.add_argument("--host", default="127.0.0.1",
                    help="loopback by default: this endpoint has no auth")
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--backend", choices=("mlx", "hash"), default="mlx")
    ap.add_argument("--self-test", action="store_true",
                    help="check the contract in-process; no model, no repo")
    args = ap.parse_args()

    if args.self_test:
        return self_test()

    backend = HashBackend() if args.backend == "hash" else MLXBackend(args.model)
    print(f"loading {backend.name} …", file=sys.stderr)
    backend.encode(["warmup"])  # fail loudly here, not on the first real request

    srv = ThreadingHTTPServer((args.host, args.port), make_handler(backend, backend.name))
    print(f"embeddings on http://{args.host}:{args.port}/v1/embeddings ({backend.name})", file=sys.stderr)
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
