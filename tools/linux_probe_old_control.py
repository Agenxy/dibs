#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.13"
# dependencies = []
# ///
"""Temporary hosted counterfactual; never part of the final fix diff."""
import subprocess
from pathlib import Path
base = "209de636c7607ca0cab8e13982b94d633fe2892c"
for name in ["internal/procinfo/process_linux.go", "internal/procinfo/process_darwin.go", "internal/liveness/discover.go", "internal/engine/supervise_loop.go"]:
    Path(name).write_bytes(subprocess.check_output(["git", "show", f"{base}:{name}"]))
result = subprocess.run(["go", "test", "-race", "-count=1", "-timeout=3m", "-v", "-run", "^TestUnknownEnvironmentCannotFeedSupervision$", "./internal/engine"], text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
print(result.stdout, flush=True)
assert result.returncode != 0, "old production unexpectedly passed"
for expected in ["unknown ownership entered supervision history", "unknown ownership retained notice suppression", "--- FAIL: TestUnknownEnvironmentCannotFeedSupervision"]:
    assert expected in result.stdout, f"not the intended runtime RED: missing {expected}"
print("INTENDED_OLD_RUNTIME_RED: both supervision assertions discriminate", flush=True)
