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
	writeWhole(path, id)
}

// writeWhole publishes id at path through a temporary file in the same
// directory, so a reader sees the old bytes or the new ones and never a
// half-written line.
//
// The temporary name comes from os.CreateTemp and NOT from the id: an id
// that has just been read out of a file is untrusted input, building a
// path from it is how a traversal gets in, and the analyser is right to
// say so even though checkNodeID has already refused anything that is
// not hexadecimal. Uniqueness was the only reason the id was in the name.
func writeWhole(path, id string) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".new-")
	if err != nil {
		return // remembering is an optimisation; failing to is not an error
	}
	tmp := f.Name()
	_, werr := f.WriteString(id + "\n")
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp)
		return
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return
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

// PendingNodeIDFile holds a Supgang identity this machine has been told
// about but is NOT serving under yet.
//
// One computer must answer to one name at a time. A bridge whose Supgang
// lookup returns after a sibling has already published a minted id keeps
// that minted id, which round forty-five settled; recording the fleet id
// in NodeIDFile there is what round fifty found, because every process
// that starts afterwards reads that file FIRST and answers with the fleet
// id while the two already running answer with the minted one. One
// machine, three bridges, two identities, and a hub that reads them as
// two computers, which is the failure all of this exists to prevent.
//
// So the late answer is written here instead, where nothing consults it
// for the identity of a running process, and the daemon promotes it at
// its next start (cmd/dibd, identifyHost): the transition belongs to a
// restart, where nothing is racing, and that start is also what renames
// the rows already registered under the old id.
const PendingNodeIDFile = "supgang_node_id.pending"

// RememberPendingNodeID records a Supgang identity for the next start
// without changing what this boot answers.
func RememberPendingNodeID(dir, id string) {
	if dir == "" || checkNodeID(id) != nil {
		return
	}
	path := filepath.Join(dir, PendingNodeIDFile)
	// #nosec G304 -- the owner's own data directory
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) == id {
		return
	}
	writeWhole(path, id)
}

// PromotePendingNodeID makes a pending identity this machine's remembered
// one and reports it, or "" when there was none. The daemon calls this at
// startup: adopting an identity is a restart's business.
func PromotePendingNodeID(dir string) string {
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, PendingNodeIDFile)
	// #nosec G304 -- the owner's own data directory
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if checkNodeID(id) != nil {
		_ = os.Remove(path) // not a node id: nothing to promote
		return ""
	}
	RememberNodeID(dir, id)
	_ = os.Remove(path)
	return id
}
