#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Hosted old-code discriminator; ordinary unchanged contracts stay green."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

CASES = ["GeneratedStandard", "UpgradeRetainsCustomBytes", "SameBuildStillNeedsMigration",
         "RefusesMalformedBeforeStop", "RefusesChangedUnit", "RefusesUnwritableBackupBeforeStop"]


def main():
    if os.environ.get("GITHUB_ACTIONS") != "true":
        raise RuntimeError("hosted proof only")
    root = Path.cwd()
    out = root / "scheduling-results" / "controls"
    out.mkdir(parents=True, exist_ok=True)
    old = Path(tempfile.mkdtemp(prefix="dibs-old-scheduling-")) / "source"
    subprocess.run(["git", "worktree", "add", "--detach", str(old), "a3fa018"], check=True)
    shutil.copy(root / "cmd/dibs/launchpolicy_darwin_test.go", old / "cmd/dibs/launchpolicy_darwin_test.go")
    rows = []
    try:
        current = subprocess.run(["go", "test", "-count=1", "-run", "^TestLaunchPolicy", "./cmd/dibs"],
                                 capture_output=True, text=True, timeout=180)
        (out / "current.txt").write_text(current.stdout + current.stderr)
        assert current.returncode == 0, "current behavioral guards failed"
        for suffix in CASES + ["OperatorAndOtherBoardControls"]:
            name = "TestLaunchPolicy" + suffix
            p = subprocess.run(["go", "test", "-count=1", "-run", "^"+name+"$", "./cmd/dibs"],
                               cwd=old, capture_output=True, text=True, timeout=180)
            (out / (name + ".txt")).write_text(p.stdout + p.stderr)
            positive = suffix == "OperatorAndOtherBoardControls"
            intended = (p.returncode == 0 if positive else p.returncode != 0 and ("--- FAIL: "+name) in p.stdout)
            rows.append({"test": name, "exit": p.returncode, "expected": "GREEN" if positive else "RED", "intended": intended})
            assert intended, f"old-code control {name} did not show its intended outcome"
    finally:
        (out / "summary.json").write_text(json.dumps(rows, indent=2))
        subprocess.run(["git", "worktree", "remove", "--force", str(old)], check=True)


if __name__ == "__main__":
    main()
