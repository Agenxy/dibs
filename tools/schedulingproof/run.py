#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Rootless, hosted-only launchd capability and real-MCP latency experiment.

No real app route, installed unit, live board or root. Run with --hosted in CI.
The experiment reports evidence; timing is not a recurring CI pass threshold.
"""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import plistlib
import re
import socket
import subprocess
import sys
import time
import urllib.request
import uuid


def run(*args, check=True):
    p = subprocess.run(args, capture_output=True, text=True, timeout=30)
    if check and p.returncode:
        raise RuntimeError(f"{args}: exit {p.returncode}: {p.stderr}")
    return p


def burn(path):
    # Separate processes make the load independent of Python's GIL. The work
    # count and process CPU time prove workers ran, rather than merely spawned.
    start = time.monotonic()
    value = b"cpu-bound scheduling fixture" * 128
    count = 0
    while True:
        for _ in range(10000):
            value = hashlib.sha256(value + b"x" * 4096).digest()
        count += 10000
        temporary = Path(path + ".tmp")
        temporary.write_text(json.dumps({"work": count, "cpu_s": time.process_time(),
                                          "wall_s": time.monotonic() - start}))
        temporary.replace(path)


class Experiment:
    def __init__(self, out, daemon):
        self.out = out
        self.daemon = daemon
        self.domain = f"gui/{os.getuid()}"
        # A missing domain is a real proof gap, never an implicit skip or a
        # fallback to a root/system service with different privilege.
        run("launchctl", "print", self.domain)
        self.targets = []
        self.workers = []
        self.rpc_id = 0

    def launch(self, tag, policy, argv, home, nice=None):
        folder = self.out / tag
        folder.mkdir()
        label = f"org.agenxy.dibs.scheduling-proof.{os.getpid()}.{tag}"
        target = f"{self.domain}/{label}"
        unit = folder / "unit.plist"
        data = {"Label": label, "ProgramArguments": argv, "RunAtLoad": True,
                "KeepAlive": False, "ProcessType": policy,
                "EnvironmentVariables": {"HOME": str(home), "PATH": os.environ["PATH"]},
                "StandardOutPath": str(folder / "stdout.log"),
                "StandardErrorPath": str(folder / "stderr.log")}
        if nice is not None:
            data["Nice"] = nice
        unit.write_bytes(plistlib.dumps(data))
        self.targets.append(target)
        result = run("launchctl", "bootstrap", self.domain, str(unit), check=False)
        (folder / "bootstrap.txt").write_text(f"exit={result.returncode}\n{result.stdout}{result.stderr}")
        if result.returncode:
            return folder, target, None
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            p = run("launchctl", "print", target, check=False)
            (folder / "launchctl.txt").write_text(p.stdout + p.stderr)
            m = re.search(r"^\s*pid = (\d+)\s*$", p.stdout, re.M)
            if m and re.search(r"^\s*state = running\s*$", p.stdout, re.M):
                pid = int(m[1])
                (folder / "ps.txt").write_text(run("ps", "-p", str(pid), "-o", "pid,ppid,pri,nice,state,%cpu,time").stdout)
                (folder / "threads.txt").write_text(run("ps", "-M", "-p", str(pid)).stdout)
                return folder, target, pid
            time.sleep(0.1)
        return folder, target, None

    def stop(self, target):
        p = run("launchctl", "bootout", target, check=False)
        if p.returncode:
            raise RuntimeError(f"fixture cleanup failed {target}: {p.stderr}")
        self.targets.remove(target)

    def rpc(self, url, secret, method, params):
        self.rpc_id += 1
        req = urllib.request.Request(url + "/mcp", data=json.dumps({
            "jsonrpc": "2.0", "id": self.rpc_id, "method": method, "params": params}).encode(),
            headers={"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
                     "MCP-Protocol-Version": "2026-07-28", "X-Dibs-Local": secret})
        with urllib.request.urlopen(req, timeout=30) as response:
            payload = json.load(response)
        if payload.get("error"):
            raise RuntimeError(f"RPC {method}: {payload['error']}")
        return payload["result"]

    def call(self, url, secret, name, args):
        result = self.rpc(url, secret, "tools/call", {"name": name, "arguments": args})
        if result.get("isError"):
            raise RuntimeError(f"tool {name}: {result}")
        payload = json.loads(result["content"][0]["text"])
        if payload.get("error") or payload.get("ok") is False:
            raise RuntimeError(f"tool {name}: {payload}")
        return payload

    def capabilities(self):
        probe = self.out / "qos-probe"
        run("clang", str(Path(__file__).with_name("qos_probe.c")), "-o", str(probe))
        results = []
        for i, (policy, nice) in enumerate([(p, None) for p in ["Background", "Standard", "Adaptive", "Interactive"]] + [("Interactive", -5)]):
            folder, target, pid = self.launch(f"cap-{i}", policy, [str(probe)], self.out, nice)
            row = {"policy": policy, "requested_nice": nice, "pid": pid}
            if pid:
                deadline = time.monotonic() + 10
                output = folder / "stdout.log"
                while time.monotonic() < deadline and (not output.exists() or not output.read_text().strip()):
                    time.sleep(0.1)
                assert output.exists() and output.read_text().strip(), "native fixture started without its QoS report"
                row["native_fixture"] = json.loads(output.read_text().splitlines()[0])
                assert row["native_fixture"]["uid"] == os.getuid() != 0
                row["loaded_policy"] = re.search(r"spawn type = (.+)", (folder / "launchctl.txt").read_text()).group(1)
                self.stop(target)
            else:
                # A refused negative-nice variant is evidence, not success.
                if nice is None:
                    raise RuntimeError(f"rootless {policy} LaunchAgent did not start")
                run("launchctl", "bootout", target, check=False)
                self.targets.remove(target)
            results.append(row)
        return results

    def start_load(self):
        for i in range(2 * os.cpu_count()):
            counter = self.out / f"burn-{i}.json"
            p = subprocess.Popen([sys.executable, str(Path(__file__).resolve()), "--burn", str(counter)],
                                 stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            self.workers.append((p, counter))
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if all(p.poll() is None and c.exists() for p, c in self.workers):
                return
            time.sleep(0.1)
        raise RuntimeError("CPU load setup did not produce live, productive workers")

    def load_snapshot(self):
        rows = []
        for p, c in self.workers:
            assert p.poll() is None, "CPU worker exited during measurement"
            row = json.loads(c.read_text())
            assert row["work"] > 0 and row["cpu_s"] > 0
            rows.append(row)
        return {"work": sum(r["work"] for r in rows), "cpu_s": sum(r["cpu_s"] for r in rows)}

    def daemon_cpu(self, pid):
        text = run("ps", "-p", str(pid), "-o", "time=").stdout.strip()
        minutes, seconds = text.split(":")
        return int(minutes) * 60 + float(seconds)

    def arm(self, tag, policy, samples, loaded):
        home = self.out / (tag + "-home")
        home.mkdir()
        board = home / "board"
        board.mkdir()
        # Reserve a loopback address without stealing any fixed service port.
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            addr = f"127.0.0.1:{sock.getsockname()[1]}"
        folder, target, pid = self.launch(tag, policy, [self.daemon, "-dir", str(board), "-addr", addr], home)
        if not pid:
            raise RuntimeError(f"real daemon {policy} did not start")
        url = "http://" + addr
        deadline = time.monotonic() + 30
        secret = None
        while time.monotonic() < deadline:
            try:
                secret = (board / "local.secret").read_text().strip()
                self.rpc(url, secret, "server/discover", {})
                break
            except (OSError, ValueError):
                time.sleep(0.1)
        assert secret, "daemon secret/readiness missing"
        agents = [self.call(url, secret, "register", {"name": f"proof-{i}", "nonce": uuid.uuid4().hex,
                   "pid": os.getpid(), "kind": "persistent"}) for i in range(2)]
        assert all(a.get("token") and a.get("agent_id") for a in agents), "register setup did not return identities"
        for a in agents:
            self.call(url, secret, "check_in", {"token": a["token"]})
        rows = []
        for _ in range(10):
            self.call(url, secret, "check_in", {"token": agents[0]["token"]})
        load_before = self.load_snapshot() if loaded else None
        cpu_before = self.daemon_cpu(pid)
        start = time.monotonic()
        for i in range(samples):
            pace = time.monotonic()
            serial = None
            for name in ["send", "respond"]:
                args = ({"token": agents[0]["token"], "to": agents[1]["agent_id"], "type": "question",
                         "body": "isolated scheduling measurement", "op_id": uuid.uuid4().hex} if name == "send" else
                        {"token": agents[1]["token"], "msg_serial": serial, "disposition": "answer", "body": "measured"})
                before = time.monotonic_ns()
                result = self.call(url, secret, name, args)
                elapsed = (time.monotonic_ns() - before) / 1e6
                if name == "send":
                    serial = result["msg_serial"]
                rows.append({"sample": i, "method": name, "rtt_ms": elapsed})
            time.sleep(max(0, 0.05 - (time.monotonic() - pace)))
        wall = time.monotonic() - start
        load_after = self.load_snapshot() if loaded else None
        cpu_after = self.daemon_cpu(pid)
        self.stop(target)
        (folder / "samples.json").write_text(json.dumps(rows))
        # Secrets and ledger keys are fixture credentials, never artifacts.
        for name in ["local.secret", "key"]:
            (board / name).unlink(missing_ok=True)
        summary = {"tag": tag, "policy": policy, "loaded": loaded, "pairs": samples,
                   "wall_s": wall, "daemon_cpu_s": cpu_after-cpu_before,
                   "errors": 0, "send": stats(rows, "send"), "respond": stats(rows, "respond")}
        if loaded:
            assert load_after["work"] > load_before["work"], "load made no progress"
            summary["competitor_work_s"] = (load_after["work"] - load_before["work"]) / wall
            summary["competitor_cpu_s"] = load_after["cpu_s"] - load_before["cpu_s"]
        return summary

    def cleanup(self):
        for p, _ in self.workers:
            p.terminate()
        for p, _ in self.workers:
            try:
                p.wait(timeout=5)
            except subprocess.TimeoutExpired:
                p.kill()
                p.wait(timeout=5)
        for target in self.targets[:]:
            run("launchctl", "bootout", target, check=False)


def stats(rows, method):
    vals = sorted(r["rtt_ms"] for r in rows if r["method"] == method)
    return {"n": len(vals), **{f"p{p}_ms": vals[min(len(vals)-1, math.ceil(p/100*len(vals))-1)] for p in [50, 95, 99]}, "max_ms": max(vals)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--hosted", action="store_true")
    parser.add_argument("--daemon")
    parser.add_argument("--out")
    parser.add_argument("--burn")
    args = parser.parse_args()
    if args.burn:
        if os.environ.get("GITHUB_ACTIONS") != "true":
            raise RuntimeError("CPU load fixture refuses a local run")
        burn(args.burn)
        return
    if not args.hosted or os.environ.get("GITHUB_ACTIONS") != "true" or sys.platform != "darwin" or os.getuid() == 0:
        raise RuntimeError("requires an explicitly opted-in, rootless hosted macOS runner")
    out = Path(args.out).resolve()
    out.mkdir(parents=True)
    result = {"uid": os.getuid(), "os": run("sw_vers").stdout, "arch": run("uname", "-m").stdout.strip(),
              "logical_cpus": os.cpu_count(), "worker_count": 2*os.cpu_count(), "arms": []}
    exp = None
    try:
        exp = Experiment(out, str(Path(args.daemon).resolve()))
        result["capabilities"] = exp.capabilities()
        for p in ["Background", "Standard", "Interactive"]:
            result["arms"].append(exp.arm(f"idle-{p}", p, 100, False))
        exp.start_load()
        for pair, policies in enumerate([["Background", "Standard"], ["Standard", "Interactive"]]):
            for round_ in range(3):
                for arm, p in enumerate([policies[0], policies[1], policies[1], policies[0]]):
                    result["arms"].append(exp.arm(f"load-{pair}-{round_}-{arm}", p, 200, True))
        result["status"] = "measured; interpret paired samples, not a universal latency guarantee"
    except Exception as exc:
        result["error"] = str(exc)
        raise
    finally:
        if exp:
            exp.cleanup()
        for home in out.glob("*-home"):
            for name in ["local.secret", "key"]:
                (home / "board" / name).unlink(missing_ok=True)
        (out / "summary.json").write_text(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
