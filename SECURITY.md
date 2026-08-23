# Security model

What Dibs protects, what it does not, and why.

This document exists because the honest answer is narrower than the feature list
implies, and a coordination tool that overstates its isolation is worse than one
that states its limits plainly.

## The trust boundary is the machine, not the agent

Dibs has **one** coordination credential: `<dir>/local.secret`, mode `0600`.
Every agent must hold it to call `/mcp` at all. That means:

- **Protected:** other users on the box, anything on the network, and any
  process that cannot read the secret file.
- **Not protected:** agents you have already configured for Dibs. They share
  one credential, so the daemon cannot cryptographically tell one from another.

An agent that holds the secret is inside the boundary. Dibs raises the cost of
one agent interfering with another, and reports honestly when it cannot prevent
it, but a malicious agent in your own fleet is not an attacker Dibs can lock
out, because you gave it the key.

**If you run agents you do not trust, do not point them at the same daemon.**
Run a second `dibd` with its own data directory; they share nothing.

## What holds inside that boundary

These are enforced, not advisory:

| Surface | Rule |
|---|---|
| Mail and acting (`/api/messages`, `/api/admin/*`, `/api/act/*`, `/api/me`) | Needs a board session **and** the page key, never the local secret alone. `/api/admin/` is gated by prefix, so a route added later is closed by default rather than open until somebody remembers |
| Board document and stream (`/`, `/events`) | The session cookie alone, because `EventSource` cannot send a header. **These two carry board state and no mail**, which is the entire reason they are separated from the row above. This table used to put them under "needs the admin password", which was wrong twice over: a cookie is sufficient here, and the session behind it is minted by Touch ID on a Mac where no admin password exists |
| Acting as the human (`/api/act/*`, `/api/me`) | Same, an agent with the secret cannot post, announce or send as the operator |
| Agent tokens | Rotated, and the previous one revoked, on register, reattach and resume. Compared in constant time, never on the board. An agent woken from `stale` or `dormant` by its own token keeps that token: it re-arms the awareness gate, not the credential |
| Blob access | Owner or recipient of a live message only; a stranger cannot learn a blob exists |
| Admin role | Granted only on the human's admin path. A role declared in `[roles]` names a string, and a name is free to take, so the declaration alone grants nothing: `[roles.identity]` must carry that agent's fingerprint, and without one the grant is refused forever rather than handed to whoever registers first. This row used to describe the superseded model, where the first registrant was pinned; that let an agent which had read `dibs.toml`, or guessed a likely line, win a two-minute race for the god view |
| Coordinator role | The admin path, and two others, both deliberate. A human may approve a `request` carrying `grant: "coordinator"`, which is the human deciding through the ordinary response path rather than a bypass of it. And on a board with NO coordinator, an agent that can read `coordinator.claim` in the data directory may take the role once: that is how a fleet with nobody at the keyboard gets one at all. The claim is single-use, is refused once any coordinator exists, and is not minted when the config has actually DECIDED who coordinates, which means a declared name with a matching `[roles.identity]` fingerprint. A bare name decides nothing, because it can never be granted, and suppressing the claim on one left a board with no coordinator and no way to get one. This row used to say "only on the human's admin path", which was false in a security document, and the direction of the error was the wrong one |
| Lineage (`parent`) | Grants nothing unless the parent vouched with a one-time nonce |
| Acting as the human from the panel (`human_unlock`) | Needs a fingerprint. The panel renders in the human's UI but speaks over the agent's connection with the agent's credential, so the transport cannot tell "the human clicked Broadcast" from "an agent called the tool". The proof has to come from outside the transport, and an agent confined to that transport cannot produce a fingerprint, see the bound below |

**A presence check proves a person is here. It does not prove they asked.**
Stated plainly because two paths were built as though it did. Any caller holding
the local secret can RAISE the prompt: `human_unlock` over MCP, and
`POST /bootstrap` over HTTP. If the operator approves, the credential goes back
to whoever asked. Nothing in the transport binds the fingerprint to the
requester, and on 0.0.6 both were reachable by an ordinary agent, one of them
with a sentence the caller wrote itself.

What holds the line now is the sentence on the sheet, so it is written by the
daemon rather than by the caller: `human_unlock` names the requesting agent,
which it can resolve from the authenticated token. That NAME is still the
agent's own, chosen at register, so it is flattened to one line of printable
characters and quoted before it reaches the prompt: a newline, a bidirectional
override or a stray quote in a display name would otherwise let an agent
rewrite what a person reads at the moment they decide. What an agent can still
do is pick a misleading name, which is the same thing it can do everywhere else
on the board and is visible as a name. `/bootstrap` says the
credential goes to whoever just asked and to decline anything the operator did
not start. That makes an unexpected prompt refusable, which is a real property
and a weaker one than binding. **If a prompt appears that you did not cause,
decline it, and treat it as a report worth making.**

**Only one prompt waits at a time, and the sheet names a code.** A refusable
prompt is no defence while two are outstanding, so `/bootstrap` serialises: a
second presence check while one is waiting is refused with 409 rather than
queued.

Serialising alone is not enough, and an earlier version of this section claimed
otherwise. First-request-wins is itself a confusion primitive: an agent leaves a
request waiting, the operator's own `dibs web` is refused, and the sheet they
then approve, at exactly the moment they expected one, completes the agent's
request. Nothing in the transport separates the two, because every agent
legitimately holds the same local secret.

So the person is the channel. `dibs web` prints a four-letter code and asks the
daemon to put it on the sheet; a prompt showing a different code, or none, was
raised by something else. The code is generated with `crypto/rand` and validated
to that exact shape before it is shown, because caller-supplied text on a
biometric prompt is the part worth attacking. **A sheet whose code does not
match the one your terminal just printed is not yours: decline it.**

### What the presence check does and does not bind

It binds the TRANSPORT. An agent speaking to Dibs over MCP cannot forge presence:
the only way to unlock is to raise a system sheet on the operator's own Mac, and
an agent that tries produces a visible prompt rather than a silent escalation.

It does not bind the MACHINE. The presence helper is resolved from the directory
the daemon was installed into. `~/.local/bin` by default, which the user owns
and can write. Code already running as that user can replace the helper with a
binary that exits zero, and the check reports Verified without a sensor being
touched. Symlinks are refused (the resolution uses `Lstat`), so the cheapest
substitution fails, but replacing the file in place still works; closing that
needs a signature check or an install root the user cannot write.

That adversary can also read the agent tokens, the ledger, and
`~/.dibs/local.secret`, so presence is not the weakest thing it defeats. An
earlier version of this document claimed software cannot produce a fingerprint.
That was wrong, and it is the kind of wrong that matters here: a security
document that overstates a bound teaches people to rely on it.

### The presence check cannot be turned off by configuration

`human_unlock` is the one place where Dibs claims a *person* did something, so
it is the one claim that must not be assertable by anything an agent can reach.
It is not gated by a flag, an environment variable, or a config key, because an
agent that can write the environment could then speak as the operator.

There is a scripted-verdict mock, because the flow is otherwise undrivable: no
test and no unattended session can produce a fingerprint, and two of the three
outcomes (*declined* and *unavailable*) cannot be reached at all on a Mac with
a working sensor. It is behind the `dibdev` **build tag**, so the code that
reads `DIBS_PRESENCE_MOCK` is not compiled into a release binary. The variable
is inert in a shipped build; there is no branch for it to take.

Two tests hold that line, and both are in the gate:

- `TestAReleaseBuildCannotBeToldAHumanIsPresent` runs in the **untagged** build,
  the one `go test ./...` and every release produce: sets the variable to
  `verified`, and asserts it changes nothing.
- `internal/mcp/e2e/human_e2e.ts` starts a real release daemon with the variable
  already set to `verified` and asserts over the real transport that it refuses.

When the mock *is* in force, every result carries `mocked: true` and a note
saying no human was checked, so a scripted unlock cannot be mistaken for
evidence that the real path works.

## What is deliberately weaker, and why

**The board session cookie reaches the board and its stream, and nothing else.**
Cookies are scoped to a *host* and never to a port, and `SameSite` does not
separate ports either, so every service you visit on `127.0.0.1` is handed
`dibs_session` by your own browser. Nothing the daemon can set on that cookie
changes this.

So the cookie is not the credential. Redeeming the magic link also hands the
browser a **page key**, in the redirect's *fragment*: browsers never send a
fragment to any server, so it appears in no request, no log and no cookie jar,
and the board keeps it in `localStorage`, which *is* scoped by port. Mail, your
identity, acting as you, and every `/api/admin/` route require it. Something
replaying the cookie can ask for the board document and gets HTML with no key in
it.

What the cookie alone still opens is the board document and `/events`, because
`EventSource` cannot send a header. Both carry board state, who is working on
what, which every agent on this machine can already see. Neither carries mail:
the document paints without it and the page fetches the mailbox over the keyed
route. That was not true when this section was first written. Both handlers
called `AllMessages`, so the document embedded every decrypted body and every
stream frame carried them, and the control described here protected nothing that
mattered.

An earlier version of this section claimed the exposure was closed by requiring
an `Origin` header on writes. That is a real control against a hostile *page*, a
browser will not let a page lie about its origin, and it is worth nothing
against a local process replaying the cookie, which writes its own headers and
declares whatever origin it likes. Both controls are present now; only the page
key stops the second one.

**The wake path is unauthenticated.** `hook_poll` and `guard_path` take a
session id and a working directory with no agent token, because a harness
lifecycle hook does not have one: that is the whole reason they exist. A caller
can therefore name any session. So these endpoints say *what* is waiting (how
many, from whom, of what kind) and never *what it says*. Reading content
requires a token.

**Reattach by session id is guessable.** Losing your context must not lose your
mailbox, so a registration presenting the same name and session id reclaims the
agent. Neither is secret: the bridge derives the session id from the host
process id. An agent registered **with a nonce** requires that nonce instead;
an agent without one is told, in its registration result, that it is reclaimable.
Pass a nonce for anything you care about.

**The wake path is a nudge, and it is deliberately free to call.** Because
`hook_poll` is token-less, a caller naming somebody else's session receives that
agent's wake summary, and nothing on that path can tell the two callers apart.

**A session id is therefore a capability on that path, and the one thing it can
spend is a wake.** This document used to say that nothing on the path consumed
or advanced anything, and that reading was repeatable so there was nothing for a
peer to spend. That was wrong. A wake is recorded as delivered when the digest
is handed out, so a caller naming somebody else's session and claiming `Stop`
receives that agent's digest AND spends its one wake; the victim's own `Stop`
then delivers nothing, and for an FYI there is no retry.

The alternative was tried and is worse. Moving the mark to the agent's own
authenticated `check_in` makes a peer unable to spend anything, and makes an
agent that reads a wake and decides not to act get interrupted by the same FYI
every turn for the rest of its life. Not being interrupted by each other is what
this product is for, and that rule has a test of its own.

So: **treat a session id as a secret**, at the same level as an agent token.
Anyone who has one can already read that agent's wake summary; they can also
cost it one delivery. Everything else on the path is still repeatable and
side-effect-free.

Two earlier designs got this wrong, and both are worth stating because the second
looked like a fix. Notices were first *deleted* on read, so a peer could destroy
them outright. They were then *throttled* on read, which only slowed it down: a
peer polling faster than the window won every eligibility point and starved the
victim indefinitely. Any timer is shared state mutated by an unidentifiable
caller. Only removing the mutation closes it.

Announcement reminders still carry a retry timer on this path, so their *nudge*
can be delayed by a peer. What no peer can touch is the fact itself: every
obligation has a **pull path** on the agent's own token-authenticated call.
`check_in` returns outstanding agent updates and `inbox` returns unread mail and
unacknowledged announcements, neither consulting wake-path state. An agent that
coordinates loses no information to a suppressor: at worst it loses the prompt
to go and look. Nothing on the token-less path reveals message or announcement
bodies.

## What is protected on disk

The ledger is the persistence *and* the audit history, so it outlives the
process and gets copied: into backups, support bundles, pasted reproductions.
Content is therefore sealed in it (AES-256-GCM under `<dir>/key`, mode `0600`)
while structure is not:

- **Sealed:** message and response bodies, agent announcements and posts, agent
  tokens, and persistent-agent nonces.
- **Not sealed:** who did what, when, to which agent: serials, op kinds, agent
  ids, topics, slot text, claimed paths. This is deliberate: `dibs verify`
  checks chain integrity without the key, and `tail -f ledger.jsonl | jq`
  stays useful.

The line is not arbitrary: **what every agent can already see is not sealed;
what only some agents can see is.** A slot declaration and an agent topic are
published to the whole board by design. `declare` is the act of telling
everyone what you are doing, and an auto-opened agent takes that declaration as
its topic. A message body and an agent announcement are scoped to a recipient or a
membership. Sealing the first would encrypt something the board displays anyway;
leaving the second unsealed is what this section exists to have fixed.

So a copied ledger reveals the *shape* of your fleet's work and not its
contents. Note what that includes: declarations are prose an agent wrote, so
"shape" here can still be a sentence describing the work. If that is sensitive
in your setting, the file mode is the boundary: treat `<dir>` as you would
`~/.ssh`.

Agent announcements were plaintext here until a reviewing agent read a candidate
build's ledger and noticed them sitting beside sealed message bodies. Both
surfaces always carried the same promise in the running daemon; only one of them
kept it on disk.

**Claims are advisory by design.** The guard enforces exclusive claims through
the harness's own pre-edit hook. An agent that ignores the hook, or edits
outside it, is not stopped. Claim expiry is loss of coordination, never proof
that work stopped.

## Reporting a vulnerability

**Privately, not as an issue.** Open a
[security advisory](https://github.com/agenxy/dibs/security/advisories/new),
that is a private space between you and the maintainer, and it stays private
until there is a fix to publish alongside it.

Include what you did, what happened, and what you expected. A reproduction
against a scratch daemon (`dibd -dir /tmp/whatever`) is worth more than a
description.

What to expect: an acknowledgement within a few days, and an honest answer about
whether it will be fixed quickly. This is a small project with no SLA, but a
vulnerability report will not sit unread, and if the answer is "this is a known
limitation, documented above" you will be told that rather than left waiting.

**Already public in this document is not a vulnerability.** Everything under
[What is deliberately weaker, and why](#what-is-deliberately-weaker-and-why) is a
considered, documented boundary: agents
sharing the local secret are inside the trust boundary, claims are advisory,
`dibs doctor` prints paths. Reports of those are welcome as issues or
discussions, not advisories.

Nothing here is a theoretical model: every limitation above was found by running
the thing, usually by an agent reviewing another agent's work.
