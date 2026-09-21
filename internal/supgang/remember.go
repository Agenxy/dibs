package supgang

import (
	"os"
	"path/filepath"
	"strings"
)

// NodeIDFile is where a Dibs data directory remembers the node id Supgang
// last gave for this computer.
//
// One machine has one identity, and both halves of Dibs derive it the same
// way: the bridge stamps it on its calls, the daemon stamps it on the rows
// of agents that reach it over loopback. A lookup that fails must not
// rename the machine for whichever half asked while it was down: the fold
// reads two ids as two machines, so its own agents stop colliding (two
// exclusive claims on one path) and its own wakes stop being local. Both
// halves therefore remember the last successful answer here and use it
// when a later lookup fails. Rounds twenty-eight and twenty-nine of the
// pre-release review, which found the two halves one round apart.
const NodeIDFile = "supgang_node_id"

// RememberNodeID records id as this machine's Supgang identity, whole and
// only when it changed. Only ever called with an answer Supgang gave.
func RememberNodeID(dir, id string) {
	if dir == "" || checkNodeID(id) != nil {
		return
	}
	path := filepath.Join(dir, NodeIDFile)
	// #nosec G304 -- the owner's own data directory
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) == id {
		return
	}
	tmp := path + ".new-" + id[:16]
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o600); err != nil {
		return // remembering is an optimisation; failing to is not an error
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

// RememberedNodeID is the last id Supgang gave for this machine, or "".
func RememberedNodeID(dir string) string {
	if dir == "" {
		return ""
	}
	// #nosec G304 -- the owner's own data directory
	b, err := os.ReadFile(filepath.Join(dir, NodeIDFile))
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if checkNodeID(id) != nil {
		return "" // not a node id: a minted one is better than a wrong one
	}
	return id
}
