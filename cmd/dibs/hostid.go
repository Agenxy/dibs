package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/paths"
	"github.com/agenxy/dibs/internal/supgang"
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
	hostIDOnce.Do(func() {
		defer func() { publishResolvedHostID(paths.DataDir(), hostIDValue) }()
		// STATED FIRST. DIBS_HOST_ID is the operator saying which computer
		// this process speaks for, for the cases where the answer below is
		// wrong: two data directories on one Supgang member standing in for
		// two machines (the two-host suite), or a container that must not
		// share the identity of the machine it runs on. A claim, as every
		// bridge's host id is (docs/NETWORK.md §2), and no stronger.
		if id := strings.TrimSpace(os.Getenv("DIBS_HOST_ID")); id != "" {
			hostIDValue = id
			return
		}
		// SUPGANG FIRST. A machine in the fleet's address plane already has
		// one identity, and it is the one every other member knows this
		// computer by; a second id minted here would make the same computer
		// answer to two names (docs/NETWORK.md §2). Only when Supgang is
		// absent or not initialised does the data directory's own id stand in.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if id, err := supgang.Status(ctx); err == nil && id.NodeID != "" {
			hostIDValue = id.NodeID
			return
		}
		hostIDValue = loadOrCreateHostID(paths.DataDir())
	})
	return hostIDValue
}

var (
	hostIDOnce  sync.Once
	hostIDValue string
)

// resolvedHostFile is where the answer above is published for the one
// reader that cannot compute it: the opencode plugin, which runs no
// subprocess and so cannot ask Supgang. It reads this file first, so the
// host it stamps on a guard is the host the bridge stamped on the
// registration, whichever source the bridge took it from. Without this the
// plugin read node_id, the bridge answered with Supgang's id on a member,
// and the daemon's host-scoped guard resolved the two to different
// machines: the guard e2e caught it on the first machine with Supgang.
const resolvedHostFile = "resolved_host_id"

// publishResolvedHostID writes the resolved id beside the secret, whole and
// only when it changed: a torn read would be a wrong host, which is worse
// than none, so the bytes land under another name and are renamed into
// place.
func publishResolvedHostID(dir, id string) {
	if dir == "" || id == "" {
		return
	}
	path := filepath.Join(dir, resolvedHostFile)
	// #nosec G304 -- the user's own data directory
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) == id {
		return
	}
	tmp := path + ".new-" + id
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o600); err != nil {
		return // a bridge that cannot publish still works; the plugin falls back
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}

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
	// PUBLISHED WHOLE AND EXCLUSIVELY, so two bridges starting together on
	// a fresh directory end up with one id. Read-then-write let each
	// generate its own, cache it for its lifetime, and overwrite the other's
	// on disk: the machine then had two identities, its agents did not
	// collide with each other, and the host bridge could not be reached for
	// the agents carrying the id that lost. The id is written to a private
	// file and linked into place: a link either succeeds, making this the
	// winner, or fails because the name exists, in which case the file there
	// is complete (a link publishes finished bytes, where an exclusive create
	// followed by a write let the loser read an empty file and keep its own
	// id, which the Linux runner did). Found by the pre-release review.
	// Named by the id it holds, which is random, rather than by pid: the
	// link shares an inode with the published file, so a second writer
	// reusing the same temporary name would truncate what everybody reads.
	tmp := path + ".new-" + id
	if err := os.WriteFile(tmp, []byte(id), 0o600); err != nil {
		// Usable for this process even if it could not be kept. A machine whose
		// id changes on restart still separates itself from other machines while
		// it runs, which is strictly better than answering "unknown" forever.
		return id
	}
	defer func() { _ = os.Remove(tmp) }()
	if err := os.Link(tmp, path); err != nil {
		if b, rerr := os.ReadFile(path); rerr == nil { // #nosec G304 -- the user's own data directory
			if won := strings.TrimSpace(string(b)); won != "" {
				return won
			}
		}
		return id
	}
	return id
}
