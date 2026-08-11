#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["mcp==2.0.0", "httpx2"]
# ///
"""Drive Dibs with the OFFICIAL MCP Python SDK, not a hand-rolled client.

Everything Dibs' 2026-07-28 support has been checked against so far was written
by the same person who wrote the server, which is the weakest possible evidence:
a shared misreading of the spec passes both sides. The reference SDK is the
independent check: it ships the discover/fallback probe and the caching model
as the spec authors implemented them.

Usage: conformance.py <url> <secret>
"""

import asyncio
import json
import sys

from mcp.client.client import Client
from mcp.client.streamable_http import streamable_http_client

GREEN, RED, YELLOW, RESET = "\033[32m", "\033[31m", "\033[33m", "\033[0m"
passed = failed = 0


def ok(m):
    global passed
    passed += 1
    print(f"  {GREEN}✓{RESET} {m}")


def no(m):
    global failed
    failed += 1
    print(f"  {RED}✗{RESET} {m}")


def _text_payload(result):
    """Most Dibs tools answer with JSON in the text content block; only the
    panel tools populate structuredContent."""
    for c in getattr(result, "content", []) or []:
        txt = getattr(c, "text", None)
        if txt:
            try:
                return json.loads(txt)
            except json.JSONDecodeError:
                pass
    return {}


async def main() -> int:
    url, secret = sys.argv[1], sys.argv[2]
    headers = {"X-Dibs-Local": secret}

    # Headers go on the http client: the transport takes a configured client
    # rather than header kwargs.
    import httpx2
    async with httpx2.AsyncClient(headers=headers, timeout=30) as http:
        # Client enters the transport itself; handing it the yielded streams
        # gives it a tuple, which is not a context manager.
        async with Client(streamable_http_client(url, http_client=http)) as client:
            # The SDK probes server/discover at the newest modern version and
            # falls back to initialize on anything that is not positive evidence
            # of a modern server. Which path it settled on is the headline.
            version = getattr(client, "protocol_version", None) or getattr(
                client, "negotiated_version", None)
            print(f"  · reference SDK negotiated: {version}")
            if version == "2026-07-28":
                ok("the official client settled on the STATELESS core")
            else:
                no(f"fell back to {version}. Dibs' discover was not accepted as modern")

            tools = await client.list_tools()
            names = {t.name for t in tools.tools}
            ok(f"tools/list returned {len(names)} tools") if names else no("no tools")

            # Cache hints, read by the SDK's own caching model rather than by me
            # asserting on raw JSON.
            ttl = getattr(tools, "ttl_ms", None)
            scope = getattr(tools, "cache_scope", None)
            if ttl is not None and scope is not None:
                ok(f"the SDK read our cache hints: ttlMs={ttl} cacheScope={scope}")
            else:
                no("the SDK saw no ttlMs/cacheScope on tools/list")

            res = await client.list_resources()
            uris = {str(r.uri) for r in res.resources}
            if any("skills" in u for u in uris):
                ok("dibs://skills is discoverable to a reference client")
            else:
                no(f"skills resource missing: {uris}")

            doc = await client.read_resource("dibs://skills")
            text = "".join(getattr(c, "text", "") for c in doc.contents)
            if "An agent is an AGENT" in text:
                ok(f"the agent playbook reads back intact ({len(text)} chars)")
            else:
                no("skills resource did not read back")

            # A full coordination round trip over the modern path.
            reg = await client.call_tool("register", {
                "name": "sdk-conformance", "description": "official MCP SDK client",
                "session_id": "s-sdk"})
            payload = reg.structured_content or _text_payload(reg)
            token = payload.get("token")
            ok("register over the stateless core") if token else no(f"register: {payload}")

            if token:
                await client.call_tool("check_in", {"token": token})
                slot = await client.call_tool("declare", {
                    "token": token, "text": "conformance run via the official SDK",
                    "refs": ["goal:v0-release"]})
                sc = slot.structured_content or _text_payload(slot)
                ok("declare accepted and echoed a slot") if sc.get("slot_id") or sc.get("ok") else no(f"declare: {sc}")

                board = await client.call_tool("check_in", {"token": token})
                b = (board.structured_content or {}).get("board", {})
                ok(f"board readable: {len(b.get('agents', []))} agent(s)") if b else no("no board")

    print(f"\n  {passed} passed, {failed} failed")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
