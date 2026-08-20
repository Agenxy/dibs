/**
 * Dibs plugin for opencode.
 *
 * Delivers Dibs mail into the session at a natural boundary: the moment a new
 * user message is assembled: by appending a synthetic text part.
 *
 * This is an in-process `fetch` from opencode's own plugin runtime. No
 * subprocess, no CLI, no polling loop. Dibs stays a service the agent pulls
 * from; this plugin only decides *when* to pull, using a hook opencode already
 * fires. See ../../PHILOSOPHY.md.
 *
 * Install: copy to ~/.config/opencode/plugin/dibs.ts (global) or
 * .opencode/plugin/dibs.ts (project-local). opencode scans
 * {plugin,plugins}/*.{ts,js}.
 *
 * Env: DIBS_ADDR (default 127.0.0.1:4777), DIBS_DIR (default ~/.dibs)
 */
import { existsSync } from "node:fs"

import type { Plugin } from "@opencode-ai/plugin"

const ADDR = process.env["DIBS_ADDR"] ?? "127.0.0.1:4777"
/**
 * Where the daemon keeps its local secret, resolved the way the daemon
 * resolves it: `~/.dibs`, falling back to a legacy `~/.agents` only when that
 * is the directory that actually exists.
 *
 * This used to default to `~/.agents` alone, and that name was never one Dibs
 * chose: the 0.0.3 rename swept `~/.lanes` up with every other "lane", and the
 * daemon then moved to `~/.dibs` and kept reading the old name for anyone who
 * had one. The plugins never moved. So on every install made since, the secret
 * was read from a directory that does not exist, `secret()` swallowed the
 * failure and returned null, and every hook here returns null on a null key.
 * The agent registered no delivery hook and nothing said so: mail simply never
 * arrived, which is the silent failure this whole plugin exists to prevent.
 */
function dataDir(): string {
  const home = process.env["HOME"] ?? "."
  const current = `${home}/.dibs`
  if (existsSync(current)) return current
  const legacy = `${home}/.agents`
  return existsSync(legacy) ? legacy : current
}

const DIR = process.env["DIBS_DIR"] ?? dataDir()

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

/** Read once and remember; the daemon rewrites it only on data-dir recreation. */
let secretCache: string | null | undefined

async function secret(): Promise<string | null> {
  if (secretCache !== undefined) return secretCache
  try {
    const f = Bun.file(`${DIR}/local.secret`)
    secretCache = (await f.text()).trim() || null
  } catch {
    secretCache = null // daemon never started here: stay quiet
  }
  return secretCache
}

/**
 * Ask Dibs whether this session may write a path.
 *
 * This is what makes a claim hold rather than merely inform. Dibs fails open
 * on every unknown, unregistered session, unclaimed path, daemon down, so a
 * deny here means a peer explicitly took an exclusive claim and is still alive.
 */
async function guard(sessionID: string, path: string): Promise<string | null> {
  const key = await secret()
  if (!key || !path) return null
  const res = await fetch(`http://${ADDR}/mcp`, {
    method: "POST",
    headers: { "content-type": "application/json", "X-Dibs-Local": key },
    body: JSON.stringify({
      jsonrpc: "2.0", id: 1, method: "tools/call",
      params: { name: "guard_path", arguments: { session_id: sessionID, path, cwd: process.cwd() } },
    }),
    // This sits in front of every edit. If Dibs is slow the edit proceeds.
    signal: AbortSignal.timeout(1500),
  })
  if (!res.ok) return null
  const body = (await res.json()) as { result?: { content?: Array<{ text?: string }> } }
  const text = body.result?.content?.[0]?.text
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
  const key = await secret()
  if (!key) return null

  const res = await fetch(`http://${ADDR}/mcp`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "X-Dibs-Local": key,
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: 1,
      method: "tools/call",
      params: {
        name: "hook_poll",
        arguments: { session_id: sessionID, event: "chat.message", cwd: process.cwd() },
      },
    }),
    // The user is waiting on their turn: never hang it on Dibs being slow.
    signal: AbortSignal.timeout(1500),
  })
  if (!res.ok) return null

  const body = (await res.json()) as {
    result?: { content?: Array<{ text?: string }> }
  }
  const text = body.result?.content?.[0]?.text
  if (!text) return null

  const payload = JSON.parse(text) as {
    hookSpecificOutput?: { additionalContext?: string }
  }
  return payload.hookSpecificOutput?.additionalContext ?? null
}

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
/**
 * The agent this session belongs to, or null.
 *
 * Same endpoint as poll() and the same session identity: hook_poll names the
 * agent whether or not it has news, precisely so a caller that wants the
 * RELATIONSHIP rather than the mail can ask for it.
 */
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
    const key = await secret()
    if (!key) return
    try {
      await fetch(`http://${ADDR}/mcp`, {
        method: "POST",
        headers: { "content-type": "application/json", "X-Dibs-Local": key },
        body: JSON.stringify({
          jsonrpc: "2.0",
          id: 1,
          method: "tools/call",
          params: {
            name: "hook_session",
            arguments: {
              session_id: SESSION,
              event: "chat.message",
              cwd: process.cwd(),
              progress: turns,
            },
          },
        }),
        signal: AbortSignal.timeout(1500),
      })
    } catch {
      // The daemon is down or slow. The turn is not ours to hold up.
    }
  })()
}

async function agent(): Promise<string | null> {
  const key = await secret()
  if (!key) return null
  try {
    const res = await fetch(`http://${ADDR}/mcp`, {
      method: "POST",
      headers: { "content-type": "application/json", "X-Dibs-Local": key },
      body: JSON.stringify({
        jsonrpc: "2.0",
        id: 1,
        method: "tools/call",
        params: {
          name: "hook_poll",
          arguments: { session_id: SESSION, event: "shell.env", cwd: process.cwd() },
        },
      }),
      // In front of every shell command the agent runs: never hang one on Dibs.
      signal: AbortSignal.timeout(1500),
    })
    if (!res.ok) return null
    const body = (await res.json()) as { result?: { content?: Array<{ text?: string }> } }
    const text = body.result?.content?.[0]?.text
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
