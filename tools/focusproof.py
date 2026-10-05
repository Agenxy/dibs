#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = []
# ///
"""Throwaway hosted proof for request42656; never part of the shipped change."""

import json
import os
from pathlib import Path
import subprocess


OLD = "d0d2191031192361630b32811437fc4e08913b30"
ROOT = Path.cwd()
LOGS = ROOT / "focus-proof-logs"
LOGS.mkdir()
ENV = dict(os.environ, DIBS_TEST_FORBID_APP_OPEN="1")
RESULTS = []


def command(argv, timeout=240):
    return subprocess.run(argv, cwd=ROOT, env=ENV, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                          timeout=timeout, check=False)


def git_text(path):
    result = command(["git", "show", f"{OLD}:{path}"])
    if result.returncode:
        raise RuntimeError(result.stdout)
    return result.stdout


def replace_once(text, before, after):
    if text.count(before) != 1:
        raise RuntimeError(f"mutation anchor count {text.count(before)}: {before!r}")
    return text.replace(before, after, 1)


def run(label, package, pattern, expected_failures, diagnostic=None):
    result = command(["go", "test", "-json", "-count=1", "-timeout=180s",
                      "-run", pattern, package])
    (LOGS / f"{label}.jsonl").write_text(result.stdout)
    failed, passed = set(), set()
    for line in result.stdout.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        name = event.get("Test")
        if name and event.get("Action") == "fail":
            failed.add(name)
        if name and event.get("Action") == "pass":
            passed.add(name)
    expected = set(expected_failures)
    if expected:
        valid = result.returncode != 0 and failed == expected
        if diagnostic:
            valid = valid and diagnostic in result.stdout
    else:
        valid = result.returncode == 0 and not failed and bool(passed)
    if not valid:
        print(result.stdout, flush=True)
        raise RuntimeError(f"{label}: exit={result.returncode}, failed={sorted(failed)}, expected={sorted(expected)}")
    row = {"label": label, "exit": result.returncode, "failed": sorted(failed),
           "passed": sorted(passed), "intended_red": bool(expected)}
    RESULTS.append(row)
    (LOGS / "summary.json").write_text(json.dumps(RESULTS, indent=2))
    print(json.dumps(row), flush=True)


ROUTES = ["TestRealShowerRoutesChatGPTToNativeBackgroundMode",
          "TestBackgroundPairContentionNeverRecordsAnAttempt",
          "TestBackgroundPairDefersAndOpensAfterRelease",
          "TestBackgroundPairSerializesDifferentBoards"]
NATIVE = "TestNativeBackgroundOpenDecisionsThroughProductionMode"
NATIVE_CASES = ["restore", "foreground", "input", "autorepeat", "active-input",
                "unknown-input", "unknown-app", "unknown-observation", "third-app",
                "intervening-app", "previous-pid-reused", "target-pid-reused",
                "unknown-target", "boundary-input", "restore-refused", "open-failed",
                "activation-timeout"]
WRAPPERS = "^TestBackgroundOpen(KeepsOldHelperWakeAndLogsFallbackOnce|KeepsWakeWithoutHelperOrCapability|NeverRetriesAmbiguousNativeOutcome)$"
run("current-go-routes", "./internal/harnessenv", "^(" + "|".join(ROUTES) + ")$", [])
run("current-native", "./internal/notify", "^" + NATIVE + "$", [])
run("current-wrapper", "./internal/notify", WRAPPERS, [])

# Restored old production bodies. The two declarations are compile-only
# carriers for symbols referenced by new tests; neither wires new behavior.
old_paths = ["internal/harnessenv/harnessenv.go", "internal/harnessenv/app_open.go",
             "internal/harnessenv/idle.go"]
saved = {path: (ROOT / path).read_text() for path in old_paths}
try:
    for path in old_paths:
        body = git_text(path)
        if path.endswith("harnessenv.go"):
            body += "\nvar appOpenFixture func(mode, url string, minIdle time.Duration) error\n"
        if path.endswith("app_open.go"):
            body += '\nvar ErrAppOpenPairBusy = errors.New("compile-only old-code carrier")\n'
            body += '\nvar backgroundPairCacheDir = os.UserCacheDir\n'
        (ROOT / path).write_text(body)
    diagnostics = ["production selector did not use the explicit fake native contact",
                   "contended pair opened app", "deferred pair outcome",
                   "different boards overlapped their desktop pair"]
    for index, name in enumerate(ROUTES):
        run(f"old-go-{index + 1}", "./internal/harnessenv", "^" + name + "$", [name], diagnostics[index])
finally:
    for path, body in saved.items():
        (ROOT / path).write_text(body)

# New cache-unavailability behavior needs a mutation of its new resolver.
pair_path = ROOT / "internal/harnessenv/app_open.go"
pair_body = pair_path.read_text()
try:
    pair_path.write_text(replace_once(pair_body,
        'return lockBackgroundPairFile(dir)', 'return nil, errors.New("mutated cache failure refusal")'))
    name = "TestUnavailableDesktopCachePreservesBoardWake"
    run("mutation-cache-failure-strands-wake", "./internal/harnessenv", "^" + name + "$", [name],
        "cache failure stranded board wake")
finally:
    pair_path.write_text(pair_body)

# The Swift file is byte-for-byte old production source. It sees --status,
# never the unknown new mode. The Go driver's private legacy port is also fake.
swift = ROOT / "internal/notify/app/notify_darwin.swift"
current_swift = swift.read_text()
try:
    swift.write_text(git_text("internal/notify/app/notify_darwin.swift"))
    run("old-native", "./internal/notify", "^" + NATIVE + "$",
        [NATIVE] + [NATIVE + "/" + name for name in NATIVE_CASES],
        "native mode missing")
finally:
    swift.write_text(current_swift)

# This wrapper did not exist before the fix. These are labeled source
# mutations of that new API, not claims about linking it on an untouched tree.
wrapper = ROOT / "internal/notify/background_open.go"
current_wrapper = wrapper.read_text()
old_helper = "TestBackgroundOpenKeepsOldHelperWakeAndLogsFallbackOnce"
missing = "TestBackgroundOpenKeepsWakeWithoutHelperOrCapability"
ambiguous = "TestBackgroundOpenNeverRetriesAmbiguousNativeOutcome"
mutations = [
    ("old-helper-no-fallback", 'return legacyBackgroundOpen(url, "native helper lacks background-open v1")',
     'return errors.New("mutated missing capability refusal")', old_helper, [old_helper]),
    ("fallback-logs-every-wake", "if !backgroundFallbackNotice.logged {", "if true {",
     old_helper, [old_helper]),
    ("missing-helper-no-fallback", 'return legacyBackgroundOpen(url, "matching native helper is unavailable")',
     'return errors.New("mutated missing helper refusal")', missing + "/missing-helper",
     [missing, missing + "/missing-helper"]),
    ("capability-failure-no-fallback", 'return legacyBackgroundOpen(url, "native helper capability could not be determined")',
     'return errors.New("mutated capability failure refusal")', missing + "/capability-timeout",
     [missing, missing + "/capability-timeout"]),
    ("lost-reply-retry", 'return fmt.Errorf("native background open failed; mail remains queued: %w", err)',
     'return legacyBackgroundOpen(url, "mutated lost-reply retry")', ambiguous + "/lost-reply",
     [ambiguous, ambiguous + "/lost-reply"]),
    ("bad-receipt-retry", 'return errors.New("native background-open receipt unknown; do not retry the open, inspect the helper installation")',
     'return legacyBackgroundOpen(url, "mutated unknown-receipt retry")', ambiguous + "/bad-receipt",
     [ambiguous, ambiguous + "/bad-receipt"]),
]
try:
    for label, before, after, name, failures in mutations:
        body = replace_once(current_wrapper, before, after)
        if label == "lost-reply-retry":
            # Removing the sole fmt use must not turn the intended RED into
            # a compile failure. This import edit has no runtime semantics.
            body = replace_once(body, '\t"fmt"\n', "")
        wrapper.write_text(body)
        run("mutation-" + label, "./internal/notify", "^" + name + "$", failures)
finally:
    wrapper.write_text(current_wrapper)

print("All current controls GREEN; restored-old and mutation controls intended RED.", flush=True)
