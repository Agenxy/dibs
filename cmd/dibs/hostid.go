package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/agenxy/dibs/internal/paths"
)

// hostID is this COMPUTER's identity, as a key rather than as its hostname.
//
// The bridge attaches it to every tool call so a daemon on another machine can
// tell this machine's absolute paths apart from its own. A hostname cannot do
// that job: it is mutable, duplicable across machines, and asserted, which is
// why core.AgentInfo.Host carries a warning and this is a separate field.
//
// THE DAEMON IGNORES THIS WHENEVER IT CAN. A call arriving over loopback is
// from this machine by construction, so the daemon stamps its own node id and
// never reads what was sent. This value therefore matters only for a bridge
// that joined a hub on another computer, where it is the only thing that knows,
// and where it is a claim rather than evidence: docs/NETWORK.md §2 states that
// limit and says what would lift it.
//
// node_id first, because a machine that runs a daemon already HAS an identity
// and having two would mean one computer answering to two names. A machine that
// only runs a bridge has no node_id and gets its own file beside the secret it
// was given.
func hostID() string {
	hostIDOnce.Do(func() { hostIDValue = loadOrCreateHostID(paths.DataDir()) })
	return hostIDValue
}

var (
	hostIDOnce  sync.Once
	hostIDValue string
)

func loadOrCreateHostID(dir string) string {
	if dir == "" {
		return ""
	}
	// The daemon's own identity, when this machine runs one.
	if b, err := os.ReadFile(filepath.Join(dir, "node_id")); err == nil { // #nosec G304 -- the user's own data directory
		if id := strings.TrimSpace(string(b)); id != "" {
			return id
		}
	}
	path := filepath.Join(dir, "host_id")
	if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- the user's own data directory
		if id := strings.TrimSpace(string(b)); id != "" {
			return id
		}
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		// EMPTY IS A SAFE ANSWER and a wrong one is not. Unknown makes the fold
		// collide exactly as it did before host ids existed; a constant or a
		// hostname would make two machines look like one, or one look like two,
		// and the second of those silently switches off real conflicts.
		return ""
	}
	id := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		// Usable for this process even if it could not be kept. A machine whose
		// id changes on restart still separates itself from other machines while
		// it runs, which is strictly better than answering "unknown" forever.
		return id
	}
	return id
}
