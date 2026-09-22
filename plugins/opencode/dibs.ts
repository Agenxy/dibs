/**
 * Dibs plugin for opencode.
 *
 * Delivers Dibs mail into the session at a natural boundary: the moment a new
 * user message is assembled: by appending a synthetic text part.
 *
 * THIS FILE IS A TRANSPORT, NOT A CLIENT. It speaks JSON-RPC over a pipe to
 * `dibs mcp-stdio`, the same bridge every other harness connects through, and
 * that binary decides everything about what a call carries: which daemon to
 * dial (including a hub that has moved), the local secret, the TLS trust
 * store, which computer this is, which checkout, and how a path is spelled.
 * It used to re-implement all of that here, and the pre-release review spent
 * a dozen rounds handing this file, one at a time, rules the Go bridge
 * already had. One implementation of a rule is the fix; this is it.
 *
 * What stays here is what only opencode can know: its own session, and the
 * hook that decides *when* to pull. Dibs stays a service the agent pulls
 * from. See ../../PHILOSOPHY.md.
 *
 * Install: copy to ~/.config/opencode/plugin/dibs.ts (global) or
 * .opencode/plugin/dibs.ts (project-local). opencode scans
 * {plugin,plugins}/*.{ts,js}.
 *
 * Env: DIBS_BIN (the dibs binary, default: on PATH). DIBS_ADDR, DIBS_DIR and
 * DIBS_HOST_ID are read by that binary rather than by this file, so whatever
 * `dibs mcp-config` writes for the bridge works here unchanged.
 */

import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process"
import type { Plugin } from "@opencode-ai/plugin"

/**
 * The dibs binary, which is this plugin's entire client: DIBS_ADDR and
 * DIBS_DIR are read by it rather than here, so whatever `dibs mcp-config`
 * writes for the bridge works unchanged, including an https:// origin whose
 * certificate is checked against the store `dibs trust` recorded.
 */
const BIN = process.env["DIBS_BIN"] ?? "dibs"

/**
 * The name Dibs knows this session by.
 *
 * NOT opencode's `input.sessionID`. That id is real, but the agent was not
 * registered under it: registration goes through the `dibs mcp-stdio` bridge,
 * which opencode spawns as a subprocess and hands no session identifier at all.
 * So the bridge names the session after the process that spawned it, and that
 * process is opencode itself, this one. `process.pid` here is the bridge's
 * `os.Getppid()` there; both sides observe the same number without ever talking.
 *
 * Sending opencode's own id instead is what made the guard useless in practice:
 * the daemon could not match it to an agent, fell open the way it is designed to,
 * and the agent overwrote a file another agent held exclusively.
 *
 * Note this is deliberately per-PROCESS, not per-conversation. `opencode run`
 * is one process for one session; the TUI can hold several conversations in one
 * process, and they share a bridge, an agent and therefore an id. That is the
 * bridge's model of a session, and the two halves agreeing matters more than
 * either half being subtler than the other.
 */
const SESSION = `host-${process.pid}`

/**
 * One call to the daemon, through a `dibs mcp-stdio` child of its own.
 *
 * A CHILD PER CALL, not a long-lived one. The first cut kept one bridge for
 * the life of the plugin, and under bun a piped child keeps the parent's
 * event loop alive however it is unref'd (`stdin.unref` does not even exist
 * there): the harness finished its turn and would not exit, and the child
 * outlived it as an orphan. Measured rather than argued: a whole cold call,
 * spawn included, is 8-17ms against a running daemon, which is nothing
 * beside the 1500ms a guard is allowed, and it leaves no lifecycle to get
 * wrong. The child is killed when the call settles, timeout included.
 *
 * The handshake rides in front of the call on the same pipe. Lines are
 * processed in order, so there is nothing to wait for; it exists to state
 * which harness this is, which the server takes from clientInfo and from
 * nowhere else.
 */
function bridgeCall(
  method: string,
  params: unknown,
  timeoutMs: number,
  client: { name: string; title: string; version: string },
): Promise<any | null> {
  let proc: ChildProcessWithoutNullStreams
  try {
    proc = spawn(BIN, ["mcp-stdio"], { stdio: ["pipe", "pipe", "ignore"] })
  } catch {
    return Promise.resolve(null) // no dibs here: the plugin is inert, the harness unharmed
  }
  return new Promise((resolve) => {
    let settled = false
    let buf = ""
    const finish = (v: any) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      try {
        proc.kill()
      } catch {
        /* already gone */
      }
      resolve(v)
    }
    const timer = setTimeout(() => finish(null), timeoutMs)
    proc.stdout.setEncoding("utf8")
    proc.stdout.on("data", (chunk: string) => {
      buf += chunk
      for (;;) {
        const nl = buf.indexOf("\n")
        if (nl < 0) break
        const line = buf.slice(0, nl).trim()
        buf = buf.slice(nl + 1)
        if (line === "") continue
        try {
          const msg = JSON.parse(line)
          if (msg?.id === 2) finish(msg)
        } catch {
          /* a line this plugin did not ask for */
        }
      }
    })
    proc.on("error", () => finish(null))
    proc.on("exit", () => finish(null))
    // AND ON THE PIPE ITSELF, which is not the same listener and is the
    // one that can take the harness down with it. A write big enough to
    // buffer completes asynchronously, so a bridge that exits meanwhile
    // (no daemon, a failed preflight) raises EPIPE on this stream AFTER
    // the try/catch below has returned; an 'error' event with no listener
    // is an uncaught exception, and Node ends the process. The plugin's
    // whole contract is that Dibs being down costs the agent nothing, and
    // without this line it costs it the session. Round sixty-three of the
    // pre-release review, reproduced with a 32 KiB body and a dibs that
    // fails preflight.
    proc.stdin.on("error", () => finish(null))
    try {
      proc.stdin.write(
        JSON.stringify({
          jsonrpc: "2.0",
          id: 1,
          method: "initialize",
          params: { protocolVersion: "2025-11-25", capabilities: {}, clientInfo: client },
        }) + "\n",
      )
      proc.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n")
      proc.stdin.write(JSON.stringify({ jsonrpc: "2.0", id: 2, method, params }) + "\n")
    } catch {
      finish(null)
    }
  })
}

/**
 * One tool call through the bridge, the envelope every hook here shares.
 * Returns the tool's text payload, or null on anything short of it: the
 * daemon down, slow, unauthorised, or answering in a shape this plugin does
 * not know. Every caller treats null as "stay quiet".
 *
 * The session id is stated in `_meta`, and the bridge keeps what a client
 * states about itself. No path and no host is touched here.
 */
async function call(name: string, args: Record<string, unknown>, timeoutMs: number): Promise<string | null> {
  const sid = typeof args["session_id"] === "string" ? (args["session_id"] as string) : SESSION
  const msg = await bridgeCall(
    "tools/call",
    { name, arguments: args, _meta: { "com.dibs/session": sid } },
    timeoutMs,
    { name: "opencode", title: "opencode", version: "" },
  )
  const text = msg?.result?.content?.[0]?.text
  return typeof text === "string" ? text : null
}

/**
 * Ask Dibs whether this session may write a path.
 *
 * This is what makes a claim hold rather than merely inform. Dibs fails open
 * on every unknown, unregistered session, unclaimed path, daemon down, so a
 * deny here means a peer explicitly took an exclusive claim and is still alive.
 */
async function guard(sessionID: string, path: string): Promise<string | null> {
  if (!path) return null
  // This sits in front of every edit. If Dibs is slow the edit proceeds.
  const text = await call("guard_path", { session_id: sessionID, path: path }, 1500)
  if (!text) return null
  const v = JSON.parse(text) as { decision?: string; reason?: string }
  // Only a hard deny stops the edit. "ask" has nowhere to go in opencode,
  // there is no permission prompt to defer to, and turning a maybe into a
  // block would wedge the fleet behind a crashed agent.
  return v.decision === "deny" ? (v.reason ?? "path is claimed by another agent") : null
}

/**
 * Ask Dibs what this session has waiting. Read-only: hook_poll never consumes
 * mail, so a dropped response loses nothing.
 */
async function poll(sessionID: string): Promise<string | null> {
  // The user is waiting on their turn: never hang it on Dibs being slow.
  const text = await call("hook_poll", { session_id: sessionID, event: "chat.message" }, 1500)
  if (!text) return null

  const payload = JSON.parse(text) as {
    hookSpecificOutput?: { additionalContext?: string }
  }
  return payload.hookSpecificOutput?.additionalContext ?? null
}

/**
 * A monotonic count of turns this process has taken, reported to Dibs.
 *
 * opencode is the one harness whose progress Dibs cannot observe from outside:
 * sessions live in SQLite, where byte growth measures WAL churn rather than
 * work, and the only append-only file is a single opencode.log SHARED by every
 * run on the machine: watching it would make every opencode agent look busy
 * whenever any one of them was.
 *
 * So this process counts for itself. The unit does not matter and the number is
 * never compared against anything but its own previous value; what matters is
 * that it only goes up while work is happening, and stops when it is not. That
 * is the difference between catching a hard stall, which CPU alone already
 * catches, and catching a slow one.
 *
 * Fire-and-forget. A supervision signal that can delay a turn is worse than a
 * missing one.
 */
let turns = 0

function reportProgress(): void {
  turns++
  void (async () => {
    try {
      await call("hook_session", { session_id: SESSION, event: "chat.message", progress: turns }, 1500)
    } catch {
      // The daemon is down or slow. The turn is not ours to hold up.
    }
  })()
}

/**
 * The agent this session belongs to, or null.
 *
 * Same endpoint as poll() and the same session identity: hook_poll names the
 * agent whether or not it has news, precisely so a caller that wants the
 * RELATIONSHIP rather than the mail can ask for it.
 */
async function agent(): Promise<string | null> {
  try {
    // In front of every shell command the agent runs: never hang one on Dibs.
    const text = await call("hook_poll", { session_id: SESSION, event: "shell.env" }, 1500)
    if (!text) return null
    const id = (JSON.parse(text) as { agent?: string }).agent
    return id && id.length > 0 ? id : null
  } catch {
    return null // daemon down, secret unreadable, shape changed: stay quiet
  }
}

const B62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
let lastMs = 0
let counter = 0

/**
 * Build a part id in opencode's own format.
 *
 * opencode validates part ids against a schema requiring the "prt" prefix, and
 * a violation does not degrade: it throws inside createUserMessage and 500s the
 * whole turn. An earlier version of this plugin used `agents-<ts>-<rand>` and
 * killed every session it touched.
 *
 * Mirrors packages/opencode/src/id/id.ts: prefix + "_" + 12 hex digits of
 * (millis << 12 | counter) + random base62 out to 26 chars. Replicated rather
 * than imported so the plugin keeps its zero-runtime-dependency property.
 */
function partID(): string {
  const ms = Date.now()
  if (ms !== lastMs) {
    lastMs = ms
    counter = 0
  }
  counter++
  const n = (BigInt(ms) * BigInt(0x1000) + BigInt(counter)) & ((BigInt(1) << BigInt(48)) - BigInt(1))
  const hex = n.toString(16).padStart(12, "0")
  let rand = ""
  for (let i = 0; i < 26 - 12; i++) rand += B62[Math.floor(Math.random() * 62)]
  return `prt_${hex}${rand}`
}

export const DibsPlugin: Plugin = async () => {
  return {
    /**
     * Stamp every shell command with the agent that issued it, so a subagent it
     * spawns can be attributed to that agent when it stalls.
     *
     * This is the cleanest of the four harness integrations, and opencode is
     * the only one that allows it. Claude Code and Codex expose a shell tool
     * whose arguments are `command`, `workdir` and `timeout`: no environment,
     * so their plugins must PREFIX an assignment onto the command string, and
     * then refuse to do so for subshells, leading redirects, multi-line scripts
     * and `cd /x && codex exec …`, because a prefix changes the meaning of each
     * one. Every refusal is a subagent attributed by a weaker signal.
     *
     * `shell.env` hands over the environment map directly. Nothing is parsed,
     * so nothing can be misparsed and there are no shapes to refuse. The
     * variable is inherited at fork and survives reparenting, daemonisation and
     * process-group changes, so it reaches every descendant however deep,
     * which is the point, since a detached child's PPID is 1 and ancestry then
     * tells you nothing.
     */
    "shell.env": async (_input: unknown, output: { env: Record<string, string> }) => {
      // Never overwrite. The OUTERMOST parent is the one that can act on a
      // stall; re-stamping at each level would reassign a child to its nearest
      // ancestor instead.
      if (output.env["DIBS_PARENT"]) return
      const id = await agent()
      if (id) output.env["DIBS_PARENT"] = id
    },

    /**
     * Refuse an edit that would trample a peer's exclusive claim.
     *
     * opencode's `tool.execute.before` returns void, so a throw is the only way
     * to stop a call: it surfaces as a tool error the model reads and can act
     * on, which is exactly the outcome wanted: the agent learns who holds the
     * path and can send them a request instead of silently clobbering them.
     *
     * Every failure path stays silent and allows. A coordination plugin that
     * breaks editing when the daemon is down is worse than no plugin.
     */
    "tool.execute.before": async (input, output) => {
      // Deliberately does NOT require input.sessionID. The guard is keyed on
      // SESSION, so demanding an id it never uses would only add a way to skip
      // the check.
      if (!/^(edit|write|patch|multiedit)$/i.test(input?.tool ?? "")) return
      const path = output?.args?.filePath ?? output?.args?.file_path ?? output?.args?.path
      if (typeof path !== "string" || !path) return
      let reason: string | null = null
      try {
        reason = await guard(SESSION, path)
      } catch {
        return // daemon down, timed out, malformed: never break the edit
      }
      if (reason) throw new Error("Dibs: " + reason)
    },

    "chat.message": async (input, output) => {
      // Counted before anything else, so a turn that fails later still counts
      // as work done: the agent was running, which is the question.
      reportProgress()
      if (!input.sessionID) return
      let context: string | null = null
      try {
        context = await poll(SESSION)
      } catch {
        return // daemon down, timed out, malformed: never break the turn
      }
      if (!context) return // no mail: inject nothing at all

      output.parts.push({
        id: partID(),
        sessionID: input.sessionID,
        messageID: output.message.id,
        type: "text",
        text: context,
        synthetic: true,
      } as (typeof output.parts)[number])
    },
  }
}

export default DibsPlugin
