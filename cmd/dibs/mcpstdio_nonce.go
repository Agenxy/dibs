package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	all := loadNonces()
	key := nonceKey(project, name)
	if all[key] == nonce {
		return
	}
	all[key] = nonce
	path := noncePath()
	if path == "" {
		return
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	// 0600: this is a credential. The directory is the daemon's own, already
	// holding the local secret and the ledger key.
	_ = os.WriteFile(path, b, 0o600) // #nosec G306 -- 0600 is the mode being set
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
			v = strings.TrimSuffix(filepath.Clean(v), string(filepath.Separator)+".git")
		}
		return filepath.Clean(v)
	}
	return ""
}

// enrichNonce is the decision, split from the JSON plumbing that calls it so it
// can be tested without a bridge, a harness, or a daemon.
//
// AGENTS.md: test the decision, not the wrapper. The wrapper is one map
// assignment; the decision is which of three sources the credential comes from,
// and it is the one with the product failure in it.
func enrichNonce(args map[string]any) {
	name, _ := args["name"].(string)
	if name == "" {
		return
	}
	project := projectKey(args)
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
