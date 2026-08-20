package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The nonce lives where the HARNESS can find it, not where the agent's context
// can.
//
// This is the product's central failure, and an agent building on top of Dibs
// stated it better than the source did: "An agent cannot be relied on to carry
// a secret across a context boundary." A persistent agent is told to keep a
// nonce, because the nonce is the only credential that survives a restart. Then
// its context ends, which is the event the nonce exists for, and the nonce goes
// with it. The next session registers under the same name with a new nonce,
// becomes a SIBLING, and cannot read a word of its predecessor's mail.
//
// Measured, on the board this was written against: nine rows for five roles.
// dibs-maintainer, -2 and -3. codex-root and -2. codex-1 and -2. web-lead and
// -2. Every one is the same event. One of them I created myself while fixing
// the others, and one agent reproduced it twice in a day, the second time
// having been warned by the very response that created the first sibling.
//
// So the bridge keeps it. The bridge is a separate process, spawned once per
// session and outliving no context: it is the only participant in this exchange
// with a memory that spans sessions and a filesystem to use. Keyed by project
// root, because "the same agent" in practice means "the same role in the same
// checkout", and that is the key a returning session can compute before the
// model has done anything at all.
//
// The agent's own word still wins. A nonce it supplies is used and stored; this
// only fills a blank, exactly like every other field the bridge supplies.

// nonceStore is the file, under the data directory rather than in the project,
// because a credential does not belong in a tree somebody might commit.
const nonceStore = "harness-nonces.json"

// rememberedNonce returns the nonce this harness last used for `name` in this
// project, or "".
func rememberedNonce(project, name string) string {
	if project == "" || name == "" {
		return ""
	}
	all := loadNonces()
	return all[nonceKey(project, name)]
}

// rememberNonce records the nonce for next time. Best effort: a failure here
// costs a sibling, which is what happened before this existed, and must never
// cost the registration itself.
func rememberNonce(project, name, nonce string) {
	if project == "" || name == "" || nonce == "" {
		return
	}
	path := noncePath()
	if path == "" {
		return
	}
	// Serialised, and written atomically.
	//
	// This was an unlocked read-modify-truncate-write of a file every bridge on
	// the machine shares, and a fleet starts many bridges at once. Two of them
	// interleaving lose each other's entries; one interrupted between truncate
	// and write leaves invalid JSON, and loadNonces treats a decode failure as
	// an empty map, so a single bad write discards EVERY remembered identity on
	// the machine. Each lost entry is a fresh nonce and therefore a sibling
	// agent that cannot read its predecessor's mail, which is the failure this
	// store exists to prevent. Found by a pre-release review.
	//
	// A lock file for the read-modify-write, and a temp-and-rename for the write
	// itself: rename is atomic, so a reader sees either the old file or the new
	// one and never a half-written one. Best effort throughout, deliberately: a
	// registration must never fail because a credential cache was busy.
	unlock := lockNonces(path)
	defer unlock()

	all := loadNonces()
	key := nonceKey(project, name)
	if all[key] == nonce {
		return
	}
	all[key] = nonce
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	// 0600: this is a credential. The directory is the daemon's own, already
	// holding the local secret and the ledger key.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".nonces-*")
	if err != nil {
		return
	}
	name2 := tmp.Name()
	defer func() { _ = os.Remove(name2) }() // no-op once the rename succeeds
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(name2, path)
}

// lockNonces serialises the read-modify-write above across bridge processes.
//
// A lock FILE rather than flock, because the bridge cross-compiles to four
// targets and the advisory-lock APIs differ across them; an exclusive create is
// the one primitive that means the same thing everywhere. Bounded: a lock older
// than the timeout is assumed to belong to a bridge that died holding it and is
// taken, because losing a nonce costs a sibling and blocking forever costs the
// registration.
func lockNonces(path string) func() {
	lock := path + ".lock"
	deadline := time.Now().Add(2 * time.Second)
	for {
		// #nosec G304 -- `lock` is noncePath() with a fixed suffix, and
		// noncePath is DIBS_DIR or ~/.dibs with a constant filename. Never
		// caller input; the same path the store beside it already uses.
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lock) }
		}
		if fi, serr := os.Stat(lock); serr == nil && time.Since(fi.ModTime()) > 10*time.Second {
			_ = os.Remove(lock) // stale: whoever held it is gone
			continue
		}
		if time.Now().After(deadline) {
			// Proceed unserialised rather than drop the identity. The write
			// below is still atomic, so the worst case is a lost entry and not
			// a corrupt file.
			return func() {}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// mintNonce generates one when neither the agent nor the store has one.
//
// 256 bits, well past the >=128 the protocol asks for. The agent never sees it
// unless it looks at its own registration, which is fine: it is the harness's
// copy, and the agent supplying its own still wins.
func mintNonce() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "" // no entropy: register without one rather than with a guessable one
	}
	return hex.EncodeToString(b)
}

func nonceKey(project, name string) string { return project + "\x00" + name }

func noncePath() string {
	dir := os.Getenv("DIBS_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".dibs")
	}
	return filepath.Join(dir, nonceStore)
}

func loadNonces() map[string]string {
	out := map[string]string{}
	path := noncePath()
	if path == "" {
		return out
	}
	b, err := os.ReadFile(path) // #nosec G304 -- a fixed filename under the data directory
	if err != nil {
		return out
	}
	if json.Unmarshal(b, &out) != nil {
		return map[string]string{}
	}
	return out
}

// projectKey is the identity of "this checkout", from what the bridge already
// discovered for the registration.
//
// The repository root where there is one, because two sessions in different
// subdirectories of one checkout are the same project and must reattach to the
// same agent. Falls back to the working directory, which is what a tree with no
// git still has. Empty when neither is known, and then nothing is remembered:
// guessing here would attach one project's identity to another.
func projectKey(args map[string]any) string {
	for _, k := range []string{"repo_dir", "cwd"} {
		v, _ := args[k].(string)
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if k == "repo_dir" {
			// .git is the marker, not the project: two worktrees of one
			// repository are one project here.
			return filepath.Clean(strings.TrimSuffix(filepath.Clean(v),
				string(filepath.Separator)+".git"))
		}
		// Resolved HERE, from cwd, because repo_dir never arrives at this point.
		//
		// This function said it keys by the repository root and listed repo_dir
		// first, which reads as though one would be supplied. Nothing supplies
		// it: the daemon fills repo_dir in during registration, and this runs in
		// the bridge, before the request is sent. So every ordinary session fell
		// through to cwd and was keyed by the exact subdirectory it started in.
		// Launching one role from the repository root and once from cmd/dibs
		// produced two agents, which is the sibling failure this whole store
		// exists to prevent, arrived at from the other side.
		//
		// Walked rather than shelled out: this is on the path of every
		// registration, and `git rev-parse` is a subprocess to answer a question
		// a directory walk answers exactly.
		return repoRootOf(filepath.Clean(v))
	}
	return ""
}

// repoRootOf walks up for the checkout containing dir, or returns dir.
//
// A worktree's `.git` is a FILE rather than a directory, and it is still the
// marker: both spellings mean "a checkout starts here".
func repoRootOf(dir string) string {
	for cur := dir; ; {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return dir // no checkout above it: the directory is the project
		}
		cur = parent
	}
}

// enrichNonce is the decision, split from the JSON plumbing that calls it so it
// can be tested without a bridge, a harness, or a daemon.
//
// AGENTS.md: test the decision, not the wrapper. The wrapper is one map
// assignment; the decision is which of three sources the credential comes from,
// and it is the one with the product failure in it.
func enrichNonce(args map[string]any, pinned string) {
	name, _ := args["name"].(string)
	if name == "" {
		return
	}
	project := projectKey(args)
	// An operator who pinned DIBS_AGENT_NONCE has said which identity this is,
	// and nothing here may argue with it.
	//
	// This ran BEFORE setPinnedIdentity attaches the header, and injected the
	// remembered nonce into the arguments. The daemon then applies its own rule
	// that an agent's stated nonce beats a transport one, correctly, and the
	// result was that the bridge's memory silently outranked the operator's
	// configuration: a session started with a pinned identity reattached to
	// whichever agent the bridge happened to remember, redirecting its mail and
	// its history while appearing to honour the config. Reproduced by a
	// pre-release review, which registered two siblings and watched the bridge
	// pick the wrong one.
	//
	// Remembered, so a later run WITHOUT the variable still reattaches to the
	// identity the operator chose, and then left alone: the header carries it.
	if pinned != "" {
		rememberNonce(project, name, pinned)
		return
	}
	if supplied, _ := args["nonce"].(string); supplied != "" {
		rememberNonce(project, name, supplied)
		return
	}
	nonce := rememberedNonce(project, name)
	if nonce == "" {
		nonce = mintNonce()
		rememberNonce(project, name, nonce)
	}
	if nonce != "" {
		args["nonce"] = nonce
	}
}
