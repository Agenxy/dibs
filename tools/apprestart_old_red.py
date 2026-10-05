#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = []
# ///
"""Hosted negative control for the exact pre-restart-feature main; never merge."""

import hashlib
import json
import os
from pathlib import Path
import subprocess


BASE = "1949755f0573f01b133b3363e2e5b0e297468ae5"
TEST_HASH = "00164eb7ac0b0ba88382f3ca0cc19bc6c7c2671612a314a32352002b6c1b2e29"
ROOT = Path.cwd()
LOGS = ROOT / "app-restart-proof-logs"
LOGS.mkdir(exist_ok=True)


def run(argv):
    return subprocess.run(argv, cwd=ROOT, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, check=False, timeout=180,
                          env=dict(os.environ, DIBS_TEST_FORBID_APP_OPEN="1"))


parent = run(["git", "rev-parse", "HEAD^"])
if parent.returncode or parent.stdout.strip() != BASE:
    raise RuntimeError(f"proof must have unchanged main {BASE} as its sole parent: {parent.stdout}")

test_path = ROOT / "internal/engine/apprestart_headline_test.go"
actual_hash = hashlib.sha256(test_path.read_bytes()).hexdigest()
if actual_hash != TEST_HASH:
    raise RuntimeError(f"old-source test differs from feature-branch test: {actual_hash}")

changes = run(["git", "diff", "--name-only", BASE, "HEAD"])
expected_changes = {
    ".github/workflows/app-restart-old-red.yml",
    "internal/engine/apprestart_headline_test.go",
    "internal/engine/apprestart_proof_carrier_test.go",
    "internal/wakeexec/apprestart_proof_carrier.go",
    "tools/apprestart_old_red.py",
}
if changes.returncode or set(changes.stdout.splitlines()) != expected_changes:
    raise RuntimeError(f"proof branch changed unexpected production source: {changes.stdout}")

result = run(["go", "test", "-json", "-count=1", "-timeout=120s",
              "-run", "^TestAppRestartEpochQueuesRecentThreadAndReportsThroughCheckIn$",
              "./internal/engine"])
(LOGS / "old-main.jsonl").write_text(result.stdout)
failed = set()
output_lines = []
for line in result.stdout.splitlines():
    try:
        event = json.loads(line)
    except json.JSONDecodeError:
        continue
    if event.get("Action") == "fail" and event.get("Test"):
        failed.add(event["Test"])
    output_lines.append(event.get("Output", ""))

name = "TestAppRestartEpochQueuesRecentThreadAndReportsThroughCheckIn"
diagnostic = 'probes=0 queue="" notice=""'
if result.returncode == 0 or failed != {name} or diagnostic not in "".join(output_lines):
    print(result.stdout, flush=True)
    raise RuntimeError(f"old-main control did not fail for missing restart delivery: exit={result.returncode}, tests={sorted(failed)}")

summary = {"base": BASE, "identical_test_sha256": actual_hash,
           "old_main_intended_red": name, "diagnostic": diagnostic}
(LOGS / "summary.json").write_text(json.dumps(summary, indent=2))
print(json.dumps(summary), flush=True)
