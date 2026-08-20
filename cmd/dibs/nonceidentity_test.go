package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The same checkout is one project, whichever subdirectory it started in.
//
// THE SIBLING THIS PREVENTS. projectKey listed `repo_dir` first and read as
// though one would be supplied, and nothing supplies it: the daemon fills
// repo_dir in during registration, and this runs in the bridge before the
// request is sent. So every ordinary session fell through to `cwd` and was
// keyed by the exact directory it started in. Launching one role from the
// repository root and once from cmd/dibs produced two agents, neither able to
// read the other's mail, which is the failure this store exists to prevent
// arrived at from the other side. Found by a pre-release review.
func TestOneCheckoutIsOneIdentityFromAnySubdirectory(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "cmd", "dibs")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	root := projectKey(map[string]any{"cwd": repo})
	deep := projectKey(map[string]any{"cwd": sub})
	if root != deep {
		t.Errorf("the same checkout keyed two ways: %q from the root, %q from a "+
			"subdirectory. Each gets its own remembered nonce, so the same role "+
			"started from two places becomes two agents", root, deep)
	}

	// A directory with no checkout above it is still its own project.
	loose := t.TempDir()
	if got := projectKey(map[string]any{"cwd": loose}); got != loose {
		t.Errorf("projectKey(%q) = %q: a tree with no git is still a project", loose, got)
	}
}

// An operator who pinned an identity is not overruled by the bridge's memory.
//
// enrichNonce runs BEFORE the pinned header is attached, and injected whatever
// it remembered into the arguments. The daemon then applies its own rule that a
// stated nonce beats a transport one, correctly, so the bridge's cache silently
// outranked the operator's configuration: a session started with a pinned
// identity reattached to whichever agent the bridge happened to remember,
// taking its mail and history with it while appearing to honour the config.
func TestAPinnedIdentityBeatsWhateverTheBridgeRemembered(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	project := map[string]any{"name": "worker", "cwd": t.TempDir()}

	// A previous session left a remembered nonce.
	rememberNonce(projectKey(project), "worker", "remembered-and-wrong")

	args := map[string]any{"name": "worker", "cwd": project["cwd"]}
	enrichNonce(args, "pinned-by-the-operator")

	if got, had := args["nonce"]; had {
		t.Errorf("the bridge injected %q into the arguments while the operator had "+
			"pinned an identity. The daemon prefers a stated nonce over the header, "+
			"so this silently wins and the agent reattaches to the wrong row", got)
	}
	// And the pinned one is remembered, so a later run without the variable
	// still reaches the identity the operator chose.
	if got := rememberedNonce(projectKey(project), "worker"); got != "pinned-by-the-operator" {
		t.Errorf("remembered %q, want the pinned identity: a run without the variable "+
			"would go back to the old row", got)
	}
}

// Concurrent bridges must not be able to discard every remembered identity.
//
// The store was an unlocked read-modify-truncate-write of a file every bridge
// on the machine shares, and a fleet starts many at once. An interrupted write
// leaves invalid JSON, and loadNonces treats a decode failure as an empty map,
// so ONE bad write discards every identity on the machine: each lost entry is a
// fresh nonce and therefore a sibling.
func TestConcurrentBridgesDoNotCorruptTheNonceStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)

	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rememberNonce("/project", string(rune('a'+i%24)), "nonce-value")
		}()
	}
	wg.Wait()

	b, err := os.ReadFile(filepath.Join(dir, nonceStore))
	if err != nil {
		t.Fatalf("the store is gone after concurrent writes: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("the store is not valid JSON after concurrent writes: %v\n"+
			"  loadNonces reads that as an empty map, so every agent on this "+
			"machine gets a new nonce and becomes a sibling", err)
	}
	if len(out) == 0 {
		t.Error("every entry was lost")
	}
}
