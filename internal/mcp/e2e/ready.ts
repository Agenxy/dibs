// Waiting for a daemon, honestly.
//
// Every suite here used to start `dibd` and then poll for `local.secret` to
// appear, treating that as "the daemon is up". It is not. The daemon writes the
// secret at cmd/dibd/main.go:152 and does not bind its listener until
// cmd/dibd/main.go:349, so the file exists for the whole of startup while
// nothing is accepting connections. The first request after the poll races the
// listen, and loses often enough to fail a CI run roughly one time in a few
// dozen: `error: Unable to connect ... code: "ConnectionRefused"`, from a suite
// whose subject was working perfectly.
//
// The readiness signal for "can I make a request" is a socket that answers a
// request. That is what this waits for.
//
// It also watches the process. A daemon that exited during startup (no local
// secret, a port already taken, a bad config) used to present as the full
// timeout followed by a connection error, which reads like a slow machine and
// sends the reader to look at timing. Handed the process, this reports the exit
// status instead, because "dibd exited with status 1" is the actual finding.

/** Options for {@link daemonReady}. */
export type ReadyOpts = {
  /** The spawned daemon, if the caller has it: lets a startup crash be
   *  reported as a crash rather than as a timeout. */
  proc?: { exited: Promise<number>; exitCode: number | null }
  /** How long to wait for each stage. Default 10s, enough for a cold binary
   *  under `-race` on a loaded machine. */
  timeoutMs?: number
  /** What to call this daemon when reporting a failure. */
  label?: string
}

const POLL_MS = 25

/** Reports whether a fetch rejection means "nothing is listening yet", as
 *  opposed to a socket that answered and then did something else. A TLS
 *  handshake failure counts as up: something accepted the connection. */
function isNotListening(err: unknown): boolean {
  const e = err as { code?: string; message?: string }
  if (e?.code === "ConnectionRefused" || e?.code === "ECONNREFUSED") return true
  return /unable to connect|connection refused|econnrefused/i.test(e?.message ?? "")
}

/**
 * Waits for a dibd to be answering requests at `base`, and returns the local
 * secret it wrote into `dir`.
 *
 * Throws rather than returning a sentinel: a suite that continues past a daemon
 * that never came up produces a page of failures whose cause is one line above
 * them, and the point of this is to put the cause where it is read first.
 */
export async function daemonReady(dir: string, base: string, opts: ReadyOpts = {}): Promise<string> {
  const timeoutMs = opts.timeoutMs ?? 10_000
  const who = opts.label ?? base

  let dead: string | null = null
  if (opts.proc) {
    void opts.proc.exited.then((code) => { dead = `dibd (${who}) exited with status ${code}` })
  }

  const deadline = Date.now() + timeoutMs

  let secret = ""
  while (!secret && Date.now() < deadline) {
    if (dead) throw new Error(`${dead} before writing local.secret`)
    try { secret = (await Bun.file(`${dir}/local.secret`).text()).trim() } catch { await Bun.sleep(POLL_MS) }
  }
  if (!secret) throw new Error(`dibd (${who}) never wrote local.secret within ${timeoutMs}ms`)

  while (Date.now() < deadline) {
    if (dead) throw new Error(`${dead} after writing local.secret, before binding ${base}`)
    try {
      await fetch(`${base}/api/hook-health`, { headers: { "X-Dibs-Local": secret } })
      return secret
    } catch (err) {
      if (!isNotListening(err)) return secret // it answered, just not with 200
      await Bun.sleep(POLL_MS)
    }
  }
  throw new Error(`dibd (${who}) wrote local.secret but never accepted a connection on ${base} within ${timeoutMs}ms`)
}
