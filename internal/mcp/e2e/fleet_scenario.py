#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""A fleet, for real: several harnesses with real models, coordinating through
Dibs, with a human acting from the board at the same time.

This is not a unit test and does not belong in `task ci`: it spends money on
model calls and depends on provider availability. It exists because everything
else in the suite drives Dibs through its own client code, and the one question
that cannot answer is whether a REAL harness, with a real model deciding what to
call, actually coordinates.

Each harness gets an isolated config. Nothing here touches the operator's own
~/.codex, ~/.hermes or ~/.config/opencode.

    ./internal/mcp/e2e/fleet_scenario.py

Stdlib only, so `uv run` needs to fetch nothing. The previous version of this
file was bash that shelled out to python3 seven times to parse its own JSON,
which is the shape of a program telling you what language it wanted to be.
"""

import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

PORT = int(os.environ.get("PORT", "4966"))
ROOT = Path(__file__).resolve().parents[3]
HOME = Path.home()
LANES = os.environ.get("DIBS", str(HOME / ".local/bin/dibs"))
LANESD = os.environ.get("DIBD", str(HOME / ".local/bin/dibd"))

GREEN, RED, YELLOW, RESET = "\033[32m", "\033[31m", "\033[33m", "\033[0m"

passed = 0
failed = 0


def ok(msg: str) -> None:
    global passed
    passed += 1
    print(f"  {GREEN}✓{RESET} {msg}")


def no(msg: str, detail: str = "") -> None:
    global failed
    failed += 1
    print(f"  {RED}✗{RESET} {msg}{'. ' + detail if detail else ''}")


def note(msg: str) -> None:
    print(f"  {YELLOW}·{RESET} {msg}")


class Daemon:
    """One dibd, its data directory, and an authenticated client for it."""

    def __init__(self, dirpath: Path, port: int, *args: str, log: Path | None = None):
        self.dir = dirpath
        self.port = port
        self.dir.mkdir(parents=True, exist_ok=True)
        self.log = log or (self.dir / "daemon.log")
        self.proc = subprocess.Popen(
            [LANESD, "-dir", str(self.dir), "-addr", f"127.0.0.1:{port}", *args],
            stdout=self.log.open("w"),
            stderr=subprocess.STDOUT,
        )
        self.secret = ""
        for _ in range(60):
            try:
                self.secret = (self.dir / "local.secret").read_text().strip()
                if self.secret:
                    break
            except OSError:
                pass
            time.sleep(0.5)

    def wait_for_matching(self, timeout: float = 60.0, step: float = 1.0) -> bool:
        """Block until the daemon says its index is built.

        Indexing is asynchronous, so a declaration made before it finishes is
        scored against an empty index and silently gets 0, which reads as
        "these two agents are unrelated" rather than "ask again in a moment".
        """
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                if "matching ready" in self.log.read_text():
                    return True
            except OSError:
                pass
            time.sleep(step)
        return False

    def call(self, tool: str, args: dict) -> dict:
        """One MCP tools/call. Returns the decoded JSON-RPC envelope."""
        body = json.dumps({
            "jsonrpc": "2.0", "id": 1, "method": "tools/call",
            "params": {"name": tool, "arguments": args},
        }).encode()
        req = urllib.request.Request(
            f"http://127.0.0.1:{self.port}/mcp", body,
            {"content-type": "application/json", "X-Dibs-Local": self.secret},
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                return json.loads(r.read().decode())
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as e:
            return {"error": {"message": str(e)}}

    def tool(self, name: str, args: dict) -> dict:
        """A tool's own result payload, decoded. {} when the call failed."""
        env = self.call(name, args)
        try:
            return json.loads(env["result"]["content"][0]["text"])
        except (KeyError, IndexError, TypeError, json.JSONDecodeError):
            return {}

    def board(self, token: str) -> dict:
        """The board, from structuredContent rather than the text payload."""
        env = self.call("board", {"token": token})
        try:
            return env["result"].get("structuredContent", {}).get("board", {})
        except (KeyError, TypeError):
            return {}

    def agent(self, name: str, **extra) -> str:
        """Register an agent and acknowledge the board. Returns its token."""
        r = self.tool("register", {"name": name, "session_id": f"s-{name}", **extra})
        tok = r.get("token", "")
        if tok:
            self.tool("check_in", {"token": tok})
        return tok

    def notice_for(self, session: str) -> str:
        """What the wake path would inject for a session, or ''."""
        d = self.tool("hook_poll", {"session_id": session, "event": "Stop"})
        return d.get("hookSpecificOutput", {}).get("additionalContext", "") or ""

    def stop(self) -> None:
        self.proc.terminate()
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    """Stop at the redirect instead of following it.

    The board hands out its session by answering `GET /?bt=<token>` with a
    303 and a Set-Cookie. urllib follows redirects by default, so the headers
    that reach the caller are the FINAL response's, and the cookie, which only
    ever existed on the 303, is silently gone. The whole human-acts-from-the-
    board half of this scenario then skips itself with "no session", which reads
    like an environment problem rather than a client bug.
    """

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


_opener = urllib.request.build_opener(_NoRedirect)


def http(method: str, url: str, *, headers: dict | None = None,
         data: dict | None = None) -> tuple[int, str, dict]:
    """A raw HTTP call to the board's own surfaces. Returns (status, body, headers)."""
    payload = json.dumps(data).encode() if data is not None else None
    req = urllib.request.Request(url, payload, headers or {}, method=method)
    if payload is not None:
        req.add_header("content-type", "application/json")
    try:
        with _opener.open(req, timeout=30) as r:
            return r.status, r.read().decode(), dict(r.headers)
    except urllib.error.HTTPError as e:
        # A 3xx arrives here now, which is the point: its headers carry the
        # cookie.
        return e.code, e.read().decode(), dict(e.headers)
    except (urllib.error.URLError, TimeoutError) as e:
        return 0, str(e), {}


def seed_project(proj: Path) -> None:
    """A small project the agents can plausibly collide over."""
    (proj / "auth").mkdir(parents=True, exist_ok=True)
    (proj / "web").mkdir(parents=True, exist_ok=True)
    (proj / "auth/middleware.go").write_text(
        "package auth\n"
        "// token validation and rate limiting for every inbound request\n"
        'func Validate(tok string) bool { return tok != "" }\n')
    (proj / "auth/retry.go").write_text(
        "package auth\n"
        "// retry/backoff around the token refresh call\n"
        "func Retry(n int) {}\n")
    (proj / "web/board.css").write_text("/* board fonts, colours and layout */\n")
    git = ["git", "-C", str(proj)]
    subprocess.run([*git, "init", "-q", "."], check=True)
    subprocess.run([*git, "add", "-A"], check=True)
    subprocess.run([*git, "-c", "user.email=e@e", "-c", "user.name=e",
                    "commit", "-qm", "auth: token validation and retry"], check=True)


PROMPT = (
    "Call lanes_register_lane with your name, then lanes_ack_board with the token, "
    'then lanes_set_slot describing your work as "fixing the token validation retry '
    'loop in auth". Report what lanes_set_slot returned, verbatim.'
)


def launch_harnesses(proj: Path, work: Path, data: Path, secret: str) -> list:
    """Start each harness in the background, isolated from the operator's config."""
    running = []

    # opencode
    (proj / "opencode.json").write_text(json.dumps({
        "$schema": "https://opencode.ai/config.json",
        "mcp": {"agents": {"type": "local", "command": [LANES, "mcp-stdio"],
                          "environment": {"DIBS_ADDR": f"127.0.0.1:{PORT}",
                                          "DIBS_DIR": str(data)},
                          "enabled": True}},
    }))
    if shutil.which("opencode"):
        env = {**os.environ, "DIBS_ADDR": f"127.0.0.1:{PORT}", "DIBS_DIR": str(data),
               "OPENCODE_CONFIG": str(proj / "opencode.json")}
        running.append(subprocess.Popen(
            ["opencode", "run", PROMPT], cwd=proj, env=env,
            stdout=(work / "opencode.log").open("w"), stderr=subprocess.STDOUT,
            stdin=subprocess.DEVNULL))
    else:
        (work / "opencode.log").write_text("opencode: not installed\n")

    # codex. Its config is overridden per-invocation with -c rather than through
    # CODEX_HOME, which this build ignores; -c MERGES with ~/.codex/config.toml,
    # so the override retargets the existing [mcp_servers.dibs] URL instead of
    # adding a stdio server beside it (which errors: "url is not supported for
    # stdio"). The operator's own config is read but never written.
    if shutil.which("codex"):
        running.append(subprocess.Popen(
            ["codex", "exec", "--skip-git-repo-check",
             "-c", f'mcp_servers.dibs.url="http://127.0.0.1:{PORT}/mcp"',
             "-c", f'mcp_servers.dibs.http_headers={{"X-Dibs-Local"="{secret}"}}',
             PROMPT],
            cwd=proj, stdout=(work / "codex.log").open("w"),
            stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL))
    else:
        (work / "codex.log").write_text("codex: not installed\n")

    # pi, via its extension.
    #
    # Credentials come from the command line rather than ~/.pi/auth.json: this
    # script must never write to the operator's own harness configuration, and
    # pi takes --provider/--model/--api-key directly. Set PI_PROVIDER/PI_MODEL/
    # PI_API_KEY (or OPENROUTER_API_KEY) to include it; without them it is
    # skipped, which is an environment fact and not a Dibs failure.
    (proj / ".pi/extensions").mkdir(parents=True, exist_ok=True)
    ext = ROOT / "plugins/pi/dibs.ts"
    if ext.exists():
        shutil.copy(ext, proj / ".pi/extensions/dibs.ts")
    key = os.environ.get("PI_API_KEY") or os.environ.get("OPENROUTER_API_KEY", "")
    if shutil.which("pi") and key:
        env = {**os.environ, "DIBS_ADDR": f"127.0.0.1:{PORT}", "DIBS_DIR": str(data)}
        running.append(subprocess.Popen(
            ["pi", "--provider", os.environ.get("PI_PROVIDER", "openrouter"),
             "--model", os.environ.get("PI_MODEL", "openai/gpt-4o-mini"),
             "--api-key", key, "--no-session", "-p", PROMPT],
            cwd=proj, env=env, stdout=(work / "pi.log").open("w"),
            stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL))
    elif shutil.which("pi"):
        (work / "pi.log").write_text(
            "pi: skipped: set PI_API_KEY (or OPENROUTER_API_KEY) to include it\n")
    else:
        (work / "pi.log").write_text("pi: not installed\n")

    return running


class Human:
    """The operator, acting from the board while the agents run.

    The board's session is minted by proving the local secret AND the admin
    password, exactly as `dibs web` does: no special path for the test.
    """

    def __init__(self, d: Daemon, password: str = "agents-fleet"):
        self.d = d
        self.cookie = ""
        self.password = password
        subprocess.run(
            [LANES, "admin", "set-password"],
            input=f"{password}\n{password}\n", text=True,
            env={**os.environ, "DIBS_ADMIN": "1", "DIBS_DIR": str(d.dir),
                 "DIBS_ADDR": f"127.0.0.1:{d.port}"},
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
        # /bootstrap returns a single-use token; the board trades it for a
        # session cookie on first load and refuses it thereafter.
        st, body, _ = http("POST", f"http://127.0.0.1:{d.port}/bootstrap",
                           headers={"X-Dibs-Local": d.secret, "X-Dibs-Admin": password})
        bt = ""
        if st == 200:
            try:
                bt = json.loads(body).get("bt", "")
            except json.JSONDecodeError:
                bt = ""
        if bt:
            _, _, hdrs = http("GET", f"http://127.0.0.1:{d.port}/?bt={bt}")
            raw = hdrs.get("Set-Cookie", "")
            self.cookie = raw.split(";")[0] if raw else ""

    @property
    def live(self) -> bool:
        return bool(self.cookie)

    def act(self, verb: str, payload: dict) -> int:
        st, _, _ = http("POST", f"http://127.0.0.1:{self.d.port}/api/act/{verb}",
                        headers={"Cookie": self.cookie}, data=payload)
        return st

    def whoami(self) -> str:
        st, body, _ = http("GET", f"http://127.0.0.1:{self.d.port}/api/me",
                           headers={"Cookie": self.cookie})
        if st != 200:
            return ""
        try:
            return json.loads(body).get("agent", "") or ""
        except json.JSONDecodeError:
            return ""


def human_acts(d: Daemon, human: Human) -> str:
    """The human joins, posts and announces. Returns its board identity."""
    if not human.live:
        note("human board actions skipped (no session)")
        return ""
    # Watching is not participating: the identity does not exist until the human
    # ACTS, so asking first correctly returns empty. Any client that asks "who am
    # I" before doing anything must handle that: the board's own UI re-asks
    # after its first action, and so does this.
    if human.whoami():
        no("lazy identity", "an agent existed before acting")
    else:
        ok("watching the board creates no agent")

    if human.act("join", {"agent": "auth-work"}) == 200:
        ok("human joined auth-work")
    else:
        no("human join", "non-200")
    me = human.whoami()
    if me:
        ok(f"acting gave the human an identity: {me}")
    else:
        no("human identity", "still empty after acting")

    if human.act("post", {"agent": "auth-work",
                          "body": "heads up: I am renaming Validate() this afternoon"}) == 200:
        ok("human posted to the agent")
    else:
        no("human post", "non-200")
    if human.act("announce", {"agent": "auth-work",
                              "body": "do not touch auth/middleware.go until I say"}) == 200:
        ok("human announced to the agent")
    else:
        no("human announce", "non-200")
    return me


def human_directs(d: Daemon, human: Human, seed_tok: str, me: str) -> None:
    """The coordinator role, then a real two-way exchange with an agent."""
    if human.live:
        # Only a human may grant the coordinator role: the director of
        # SPEC-CHANNELS.md §8.1. No agent can promote itself.
        st, _, _ = http("POST", f"http://127.0.0.1:{d.port}/api/admin/role",
                        headers={"X-Dibs-Local": d.secret,
                                 "X-Dibs-Admin": human.password},
                        data={"agent": "seed", "role": "coordinator"})
        if st == 200:
            ok("human granted seed the coordinator role")
        else:
            no("role grant", f"HTTP {st}")

        st = human.act("send", {"to": "seed", "type": "question",
                                "body": "Which file holds token validation?",
                                "deadline_s": 600})
        if st == 200:
            ok("human asked an agent a question")
        else:
            no("human send", f"HTTP {st}")

    # An agent asks the HUMAN something, and the human answers from the board.
    q = d.tool("send", {"token": seed_tok, "to": me, "type": "question",
                                "body": "May I rename Validate()?",
                                "op_id": "ask-human", "deadline_s": 600})
    serial = q.get("msg_serial", 0)
    if human.live and serial:
        st = human.act("respond", {"serial": serial,
                                   "body": "Yes, but announce it in auth-work first.",
                                   "disposition": "answer"})
        if st == 200:
            ok("human answered the agent from the board")
        else:
            no("human respond", f"HTTP {st}")
        got = d.tool("read_mail", {"token": seed_tok, "msg_serial": serial}) \
               .get("message", {}).get("response", "")
        if "announce it in auth-work" in got:
            ok("the agent can READ the human's answer")
        else:
            no("agent read the answer", f"got: {got[:60]}")


def announcement_reaches_an_agent(d: Daemon, seed_tok: str) -> None:
    """The human's announcement must reach an agent through the wake path,
    WITHOUT handing its contents to a caller that proved nothing.

    hook_poll takes a session id off the wire with no agent token, because a
    harness lifecycle hook has no token to give. So any holder of the
    coordination secret can name any session id. The wake says WHAT is waiting;
    the token is what reads it.
    """
    inj = d.notice_for("seed")
    if "unacknowledged announcement" in inj:
        ok("the human's announcement reached an agent via the wake path")
    else:
        no("announcement injection", f"got: {inj[:70]}")
    if "do not touch auth/middleware.go" in inj:
        no("wake path confidentiality",
           "the announcement BODY was handed to an unauthenticated caller")
    else:
        ok("and its contents were not disclosed to an unauthenticated caller")

    # The agent that holds the token can read it, which is the point.
    inbox = d.tool("inbox", {"token": seed_tok})
    seen = sum(1 for a in (inbox.get("announcements") or [])
               if "auth/middleware.go" in a.get("body", ""))
    if seen >= 1:
        ok("while the agent itself reads it with its own token")
    else:
        no("token-authenticated read", "the real agent could not read the announcement")


def report_board(board: dict, me: str) -> None:
    agents = [agent for agent in board.get("agents", []) if agent["id"] not in ("seed", "inspector")]
    chans = board.get("spaces", [])
    harnesses = sorted({(agent.get("agent") or {}).get("harness", "?") for agent in agents})
    print(f"\n  agents registered: {len(agents)}. "
          f"harnesses: {', '.join(h for h in harnesses if h)}")
    for agent in agents:
        a = agent.get("agent") or {}
        slot = (agent.get("slots") or [{}])[0].get("text", "")
        print(f"    {agent['id']:<22} {a.get('harness', '?'):<12} {slot[:44]}")

    auth = next((c for c in chans if c["id"] == "auth-work"), None)
    if auth:
        members = [m["agent"] for m in auth.get("members", [])]
        auto = [m["agent"] for m in auth.get("members", []) if m.get("auto")]
        scored = [f"{m['agent']}={m.get('score', 0):.3f}"
                  for m in auth.get("members", []) if m.get("score")]
        print(f"\n  agent auth-work members: {', '.join(members)}")
        print(f"    auto-matched in: {', '.join(auto) if auto else '(none)'}")
        print(f"    recorded scores: {', '.join(scored) if scored else '(none: joined explicitly)'}")
        print(f"    human present:   {'yes' if me and me in members else 'no'}")
        print(f"    unacked announcements: {auth.get('unacked_announcements', 0)}")


def director_merges(d: Daemon, seed_tok: str) -> None:
    """Granting the role proves nothing; using it does.

    A merge is the destructive one, and it has three things to decide: the
    source agent's MEMBERS, its QUEUE, and its outstanding ANNOUNCEMENTS. Two of
    those were silently dropped: a queued agent was left waiting forever on a
    agent that had been deleted, and its announcements were left naming an agent
    that no longer existed, countable on no board while still obliging members
    to answer them.
    """
    print()
    print("  the coordinator directs: merge with live members, a queue and unacked mail")

    holder = d.agent("holder")
    waiter = d.agent("waiter")
    bystander = d.agent("bystander")
    host = d.agent("host")

    # src: exclusive, held by holder, with bystander a member and waiter queued.
    d.tool("open_space", {"token": holder, "agent": "src-agent",
                         "topic": "token refresh retry loop", "exclusive": True})
    d.tool("admit", {"token": seed_tok, "agent": "src-agent", "to": "bystander"})
    qpos = d.tool("join_space", {"token": waiter, "agent": "src-agent"}).get("queue_position")
    if qpos == 1:
        ok("waiter is queued behind the exclusive owner")
    else:
        no("queue setup", f"position={qpos}")

    # An outstanding announcement, bound to bystander and unanswered.
    d.tool("announce", {"token": holder, "agent": "src-agent",
                             "body": "src-agent: freezing auth/retry.go"})
    d.tool("open_space", {"token": host, "agent": "dst-agent", "topic": "auth token work"})

    # Drain everyone's notices first, so what we read after is the merge.
    for who in ("seed", "holder", "waiter", "bystander", "host"):
        d.notice_for(f"s-{who}")
    d.notice_for("seed")

    env = d.call("merge_spaces", {"token": seed_tok, "agent": "src-agent",
                                "to": "dst-agent", "note": "same work"})
    if env.get("result", {}).get("isError"):
        no("coordinator merged two agents", json.dumps(env)[:120])
    else:
        ok("the coordinator merged src-agent into dst-agent")

    board = d.board(seed_tok)
    dst = next((c for c in board.get("spaces", []) if c["id"] == "dst-agent"), {})
    members = sorted(m["agent"] for m in dst.get("members", []))
    unacked = dst.get("unacked_announcements", 0)
    gone = not any(c["id"] == "src-agent" for c in board.get("spaces", []))
    print(f"    dst-agent after merge: members={members} unacked={unacked} src-gone={gone}")

    # 1. The queued agent went somewhere real.
    if "waiter" in members:
        ok("the agent queued on the deleted agent was carried, not dropped")
    else:
        no("queue carried", f"waiter vanished with src-agent: {members}")
    if gone:
        ok("the source agent is gone")
    else:
        no("source deleted", "src-agent still present")

    # 2. The outstanding announcement follows the work, and is countable again.
    if unacked == 0:
        no("announcement carried", "still invisible on every board")
    else:
        ok("the outstanding announcement is visible on the surviving agent")

    # ...and an agent can PULL it, not just be pushed it. An obligation that only
    # arrives through the wake path is one an agent without a plugin never sees,
    # and one a context-lost agent cannot ask about.
    anns = [x for x in (d.tool("inbox", {"token": bystander}).get("announcements") or [])
            if "freezing auth/retry.go" in x.get("body", "").lower()]
    if not anns:
        no("announcement pull", "the bystander cannot see what it owes")
    elif anns[0].get("agent") == "dst-agent" and "ack_announcement" in (anns[0].get("action") or ""):
        ok("the carried announcement is pullable from the inbox, on the surviving agent")
    else:
        no("announcement pull",
           f"wrong agent or no instruction: {anns[0].get('agent')} {anns[0].get('action', '')[:40]}")

    # The recovery path: an agent that lost everything calls check_in. It must
    # return BOTH kinds of thing an agent cannot reconstruct for itself: what it
    # owes somebody (announcements) and what was done to it (agent_updates).
    #
    # agent_updates is the authoritative path for the second. The wake hook is a
    # nudge, and because it is token-less a peer that merely knows a session id
    # can keep somebody else's nudges quiet indefinitely, so a fact that ONLY
    # ever arrived that way was a fact another agent could suppress.
    rec = d.tool("check_in", {"token": bystander})
    owes = len([x for x in (rec.get("announcements") or [])
                if "freezing" in x.get("body", "").lower()])
    moved = len([x for x in (rec.get("agent_updates") or []) if "src-agent" in json.dumps(x)])
    if owes == 0:
        no("recovery checkpoint", "check_in omitted the outstanding announcement")
    elif moved == 0:
        no("recovery checkpoint", "check_in omitted what was DONE to the agent")
    else:
        ok("and check_in tells a context-lost agent what it owes AND what was done to it")

    # 3. THE point: every agent the director moved was told. The two that never
    # called check_in are told through the wake path; the bystander already had
    # it from check_in above, which is why its notice is gone: delivered, not
    # lost.
    for who in ("holder", "waiter"):
        n = d.notice_for(f"s-{who}")
        if "src-agent" in n and "no longer exists" in n:
            ok(f"{who} was told src-agent is gone and where its work went")
        else:
            no(f"{who} was told", f"got: {n[:90].replace(chr(10), ' ')}")


def director_evicts(d: Daemon, seed_tok: str) -> None:
    """Eviction used to check membership only, so removing a QUEUED agent
    answered "not a member": the director concluded the agent was not on the
    agent and moved on, and the moment the owner left, the agent it had tried to
    remove was promoted to OWNER of that agent.
    """
    owner = d.agent("ev-owner")
    q1 = d.agent("ev-q1")
    q2 = d.agent("ev-q2")
    d.tool("open_space", {"token": owner, "agent": "ev-agent",
                         "topic": "exclusive work", "exclusive": True})
    d.tool("join_space", {"token": q1, "agent": "ev-agent"})
    d.tool("join_space", {"token": q2, "agent": "ev-agent"})
    d.notice_for("s-ev-q1")

    if d.tool("evict", {"token": seed_tok, "agent": "ev-agent",
                             "to": "ev-q1", "note": "wrong agent"}).get("evicted"):
        ok("a coordinator can evict an agent that was only queued")
    else:
        no("queue eviction", "the director is told 'not a member'")

    n = d.notice_for("s-ev-q1")
    if "queue for agent" in n and "will not be admitted" in n:
        ok("and it is told waiting is over, not to 'stop work'")
    else:
        no("queue-eviction notice", f"got: {n[:90].replace(chr(10), ' ')}")

    # The consequence: the owner leaves, and the evicted agent must NOT inherit.
    d.tool("leave_space", {"token": owner, "agent": "ev-agent"})
    ev = next((c for c in d.board(seed_tok).get("spaces", []) if c["id"] == "ev-agent"), {})
    new_owner = ev.get("owner", "")
    if new_owner == "ev-q1":
        no("evicted agent promoted", "the agent the director removed now OWNS ev-agent")
    elif new_owner == "ev-q2":
        ok("the next real waiter was promoted, not the evicted one")
    else:
        note(f"ev-agent owner after release: '{new_owner or 'none'}'")


def subagent_inherits(d: Daemon) -> None:
    """SPEC-CHANNELS.md §8.2: subagents inherit their parent's agents.

    Letting one JOIN was not merely redundant: a subagent asking for the agent
    its parent held exclusively was queued behind that parent, and the parent
    does not release until the subagent's work is done. Each waits on the other.
    """
    nonce = "fleet-child-0123456789abcdef"
    par = d.agent("parent-agent")
    d.tool("open_space", {"token": par, "agent": "par-agent",
                         "topic": "work the parent owns", "exclusive": True})
    # `parent` alone is a claim anybody can make, and a subagent inherits its
    # parent's memberships, skips an exclusive space's queue and is exempt from
    # the parent's exclusive claims in the guard, so lineage is proven with a
    # one-time nonce only the parent can issue.
    d.tool("vouch_child", {"token": par, "nonce": nonce})
    sub = d.agent("sub-agent", parent="parent-agent", parent_nonce=nonce)

    joined = d.tool("join_space", {"token": sub, "agent": "par-agent"})
    if joined.get("queued"):
        no("subagent deadlock", "the subagent was queued behind its own parent")
    else:
        ok("a subagent inherits its parent's agent instead of queueing behind it")
    if joined.get("under") == "parent-agent":
        ok("and is told whose membership it is acting under")
    else:
        no("inheritance attribution", f"under='{joined.get('under')}'")

    # The thing the inherited membership is FOR must still work.
    serial = d.tool("post", {"token": sub, "agent": "par-agent",
                                  "body": "subagent reporting"}).get("serial")
    if serial:
        ok("and can speak in the agent under that membership")
    else:
        no("subagent post", f"got serial '{serial}'")

    # An agent that merely CLAIMS the parent gets none of it. Before lineage was
    # proven, this was a live escalation: registering with parent:<victim> let an
    # agent post into the victim's exclusive space, skip its queue, and write
    # inside its exclusive claim.
    fake = d.agent("impostor", parent="parent-agent")
    if d.tool("join_space", {"token": fake, "agent": "par-agent"}).get("queued"):
        ok("an unvouched lineage is queued like any stranger")
    else:
        no("unvouched lineage", "an agent that merely claimed a parent inherited its agent")


def crashed_agent_never_inherits(work: Path) -> None:
    """The promotion check tested `closed` only, but an agent that CRASHES is
    `stale`. So a dead agent was handed exclusive ownership while healthy agents
    queued behind a corpse. sign_off dequeues, which is exactly why this hid:
    it only ever showed up for real crashes.

    A crash here is a real one (the lease lapses and the sweep notices) on its
    own daemon with a short lane_ttl, so the rest of the fleet is not aged out
    underneath this check.
    """
    cdir = work / "crash"
    cdir.mkdir(parents=True, exist_ok=True)
    (cdir / "dibs.toml").write_text('[limits]\nlane_ttl = "6s"\n')
    d = Daemon(cdir, PORT + 9, log=cdir / "log")
    if not d.secret:
        no("crash daemon", "never started")
        d.stop()
        return
    ok("a daemon honours [limits] lane_ttl from dibs.toml")
    try:
        owner = d.agent("cr-owner", pid=os.getpid())
        crashed = d.agent("cr-crashed", pid=os.getpid())
        live = d.agent("cr-live", pid=os.getpid())
        d.tool("open_space", {"token": owner, "agent": "cr-agent",
                             "topic": "exclusive work", "exclusive": True})
        # cr-crashed goes silent and stays silent from here. cr-owner and
        # cr-live keep their leases alive, exactly as a working agent does with
        # any authenticated call.
        d.tool("join_space", {"token": crashed, "agent": "cr-agent"})
        d.tool("join_space", {"token": live, "agent": "cr-agent"})
        for _ in range(14):
            d.tool("heartbeat", {"token": owner})
            d.tool("heartbeat", {"token": live})
            time.sleep(1)

        status = next((agent.get("status", "") for agent in d.board(owner).get("agents", [])
                       if agent["id"] == "cr-crashed"), "")
        if status != "stale":
            no("crash detection", f"cr-crashed is '{status}' after 14s at lane_ttl=6s")
            return
        ok("a silent agent is detected as crashed (stale), while busy ones stay active")
        d.tool("leave_space", {"token": owner, "agent": "cr-agent"})
        cr = next((c for c in d.board(live).get("spaces", []) if c["id"] == "cr-agent"), {})
        print(f"    cr-agent after the owner left: owner={cr.get('owner', '')!r} "
              f"queue={cr.get('queue', [])}")
        if cr.get("owner") == "cr-crashed":
            no("crashed agent promoted", "the agent is locked behind a crashed agent")
        elif cr.get("owner") == "cr-live":
            ok("the crashed agent was skipped and the live waiter took the agent")
        else:
            no("promotion", f"unexpected owner: {cr.get('owner', '')!r}")
    finally:
        d.stop()


def probe_separation(d: Daemon, label: str, agent: str = "auth-work") -> None:
    """Declarations that SHOULD match, and ones that should not.

    Printed rather than asserted at a fixed number: the point is to see the
    separation, and the absolute scores move with the project's own history.
    """
    probes = [
        "fixing the retry backoff around token refresh",
        "rate limiting on inbound requests",
        "validating bearer tokens in middleware",
        "restyling the board fonts and CSS layout",
        "writing release notes for the changelog",
    ]
    for i, text in enumerate(probes):
        tok = d.agent(f"{label}-{i}")
        r = d.tool("declare", {"token": tok, "text": text})
        hit = [x for x in (r.get("agents") or []) if x["agent"] == agent]
        score = f"{hit[0]['score']:.3f}" if hit else " ,  "
        print(f"    {score}  {text}")


def embeddings_available(url: str, model: str) -> bool:
    st, _, _ = http("POST", f"{url}/v1/embeddings",
                    data={"model": model, "input": ["probe"]})
    return st == 200


def main() -> int:
    work = Path(tempfile.mkdtemp(prefix="agents-fleet-"))
    data, proj = work / "data", work / "project"
    proj.mkdir(parents=True, exist_ok=True)
    daemons: list[Daemon] = []

    def cleanup(*_):
        for dm in daemons:
            try:
                dm.stop()
            except Exception:
                pass
        shutil.rmtree(work, ignore_errors=True)

    signal.signal(signal.SIGINT, lambda *a: (cleanup(), sys.exit(130)))

    try:
        seed_project(proj)
        d = Daemon(data, PORT, "-match-repo", str(proj),
                   "-match-join", "0.25", "-match-notify", "0.10",
                   log=work / "daemon.log")
        daemons.append(d)
        if not d.secret:
            print("daemon never started")
            return 1
        d.wait_for_matching()

        print()
        print(f"fleet scenario. {proj} on 127.0.0.1:{PORT}")
        print("────────────────────────────────────────────────────────────")

        # session_id "seed", not the helper's "s-seed": the wake-path checks
        # below poll this session by name, and a mismatch makes hook_poll answer
        # about a session that does not exist, which looks exactly like "the
        # announcement never reached the agent" rather than "you asked about the
        # wrong agent".
        seed_tok = d.agent("seed", session_id="seed")
        d.tool("open_space", {"token": seed_tok, "agent": "auth-work",
                             "topic": "token validation, retry and rate limiting in auth"})
        ok("seeded agent auth-work")

        running = launch_harnesses(proj, work, data, d.secret)
        human = Human(d)
        me = human_acts(d, human)
        human_directs(d, human, seed_tok, me)
        announcement_reaches_an_agent(d, seed_tok)

        for p in running:
            try:
                p.wait(timeout=900)
            except subprocess.TimeoutExpired:
                p.kill()

        inspector = d.agent("inspector")
        board = d.board(inspector)
        report_board(board, me)

        director_merges(d, seed_tok)
        director_evicts(d, seed_tok)
        subagent_inherits(d)
        crashed_agent_never_inherits(work)

        # ── assertions on what the real harnesses did ──────────────────────
        agents = [agent for agent in board.get("agents", [])
                  if agent["id"] not in ("seed", "inspector", me)]
        harnesses = {(agent.get("agent") or {}).get("harness", "") for agent in agents}
        harnesses.discard("")
        auth = next((c for c in board.get("spaces", []) if c["id"] == "auth-work"), {})
        members = [m["agent"] for m in auth.get("members", [])]
        auto = [m["agent"] for m in auth.get("members", []) if m.get("auto")]

        if len(harnesses) >= 2:
            ok(f"{len(harnesses)} distinct harnesses registered on one board")
        else:
            no("multiple harnesses", f"only {len(harnesses)}")
        if len(auto) >= 1:
            ok(f"{len(auto)} agent(s) auto-matched into the agent by their own words")
        else:
            note("no auto-match (models may not have declared work)")
        if me and me in members:
            ok("the human is a member alongside the agents")
        else:
            no("human membership", "not a member")
        if auth.get("unacked_announcements", 0) >= 1:
            ok("the human's announcement is outstanding against members")
        else:
            note("announcement had no other members to bind")

        verified = subprocess.run([LANES, "verify", str(data / "ledger.jsonl")],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if verified.returncode == 0:
            ok("ledger chain intact after the whole run")
        else:
            no("ledger", "verify failed")

        # ── how good is the matching, really? ──────────────────────────────
        print()
        print("  semantic separation (auth-work topic: token validation, retry, rate limiting)")
        probe_separation(d, "probe")
        print("    (tier 0 matches on predicted FILES, so topic words that appear in no")
        print("     filename ('rate limiting' lives in a comment) are invisible to it.)")

        embed_url = os.environ.get("EMBED_URL", "http://127.0.0.1:11434")
        embed_model = os.environ.get("EMBED_MODEL", "qwen3-embedding:0.6b")
        if embeddings_available(embed_url, embed_model):
            print()
            print(f"  same probes, tier 2 ({embed_model}): content, not paths")
            e = Daemon(work / "embed", PORT + 7, "-match-repo", str(proj),
                       "-match-join", "0.25", "-match-notify", "0.10",
                       "-match-embed-url", embed_url, "-match-embed-model", embed_model,
                       log=work / "embed.log")
            daemons.append(e)
            e.wait_for_matching(timeout=180, step=2)
            etok = e.agent("eseed")
            e.tool("open_space", {"token": etok, "agent": "auth-work",
                                 "topic": "token validation, retry and rate limiting in auth"})
            probe_separation(e, "ep")
            e.stop()
        else:
            print("  (tier 2 comparison skipped: no embeddings service)")

        print("────────────────────────────────────────────────────────────")
        print(f"  {passed} passed, {failed} failed")
        for h in ("opencode", "codex", "pi"):
            tail = ""
            try:
                tail = " ".join((work / f"{h}.log").read_text().splitlines()[-3:])[:90]
            except OSError:
                pass
            print(f"  {h:<9} {tail}")
        return 0 if failed == 0 else 1
    finally:
        cleanup()


if __name__ == "__main__":
    sys.exit(main())
