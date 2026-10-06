#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Supplemental rootless thread-API probe; never a claim about Go's writer."""
import json
import os
from pathlib import Path
import sys
import time
from run import Experiment, run


def main():
    if os.environ.get("GITHUB_ACTIONS") != "true" or sys.platform != "darwin" or os.getuid() == 0:
        raise RuntimeError("requires rootless hosted macOS")
    out = Path("scheduling-results/qos-api").resolve()
    out.mkdir(parents=True)
    probe = out / "request-qos"
    run("clang", str(Path(__file__).with_name("request_qos.c")), "-o", str(probe))
    exp = Experiment(out, "")
    rows = []
    try:
        for policy in ["Background", "Standard", "Interactive"]:
            folder, target, pid = exp.launch(policy, policy, [str(probe)], out)
            assert pid, "native QoS fixture failed to start"
            output = folder / "stdout.log"
            deadline = time.monotonic()+10
            while time.monotonic() < deadline and (not output.exists() or not output.read_text().strip()):
                time.sleep(0.1)
            result = json.loads(output.read_text().splitlines()[0])
            assert result["uid"] == os.getuid() != 0
            rows.append({"policy": policy, "native_thread_only": result})
            exp.stop(target)
    finally:
        exp.cleanup()
        (out / "summary.json").write_text(json.dumps(rows, indent=2))


if __name__ == "__main__":
    main()
