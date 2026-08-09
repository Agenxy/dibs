# Lanes for OpenClaw

**Deferred.** Not surveyed, not cloned, not measured.

Every other platform in this directory was investigated by reading its source and
driving it. OpenClaw has had none of that, so there is nothing here worth
asserting — and a plausible-looking config block for a harness nobody has run is
worse than an empty page.

When it is picked up, the order that worked elsewhere:

1. Clone the source; never infer capabilities from a shipped binary's strings
   (that mistake is recorded in [WAKE-MECHANISMS.md](https://github.com/agenxy/lanes/blob/main/WAKE-MECHANISMS.md) §3).
2. Probe a live handshake with `LANES_LOG_RPC=1` — protocol version, declared
   capabilities, which methods it actually sends.
3. If it is an MCP client, a config entry is the whole integration.
4. Only then look for a wake path, and only accept one that needs no subprocess.
5. Drive it live before claiming anything works.
