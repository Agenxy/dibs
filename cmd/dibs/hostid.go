package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
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
		dir := paths.DataDir()
		defer func() { publishResolvedHostID(dir, hostIDValue) }()
		hostIDValue = resolveHostID(dir)
	})
	return hostIDValue
}

// resolveHostID is the decision hostID memoises, without the memoisation:
// one process asks once, and everything on this machine has to reach the
// same answer. Separated so it can be tested, because a sync.Once cannot.
func resolveHostID(dir string) string {
	// STATED FIRST. DIBS_HOST_ID is the operator saying which computer
	// this process speaks for, for the cases where the answer below is
	// wrong: two data directories on one Supgang member standing in for
	// two machines (the two-host suite), or a container that must not
	// share the identity of the machine it runs on. A claim, as every
	// bridge's host id is (docs/NETWORK.md §2), and no stronger.
	if id := strings.TrimSpace(os.Getenv("DIBS_HOST_ID")); id != "" {
		return id
	}
	// THE DATA DIRECTORY'S RECORDED IDENTITY, and Supgang seeds it.
	//
	// What every process on one machine must agree about is a single
	// value at any instant, and a bridge holds its answer for its
	// life. So the answer is a FACT ON DISK, read the same way by
	// everything here (the daemon reads the same two files, and the
	// plugins read what this publishes), and Supgang is consulted only
	// when the directory has nothing to say:
	//
	//   - the id Supgang gave here before (supgang.NodeIDFile), which
	//     a later failed lookup therefore cannot rename (round
	//     twenty-eight);
	//   - the daemon's own node id, or a host id minted here earlier,
	//     which is what a machine with no Supgang answers by;
	//   - and only then Supgang itself, remembered so the next process
	//     needs no lookup.
	//
	// Asking Supgang FIRST looked right and was the same split from
	// the other side: the first bridge started after Supgang became
	// available answered with the fleet id while every older bridge,
	// and the running daemon, still answered with the minted one, and
	// the fold reads two ids as two machines, so two agents on one
	// computer each took an exclusive claim on the same path outside a
	// checkout. A machine JOINING the fleet therefore adopts its fleet
	// identity when the daemon there next starts (it remembers the
	// answer for everything else), not the instant a lookup succeeds.
	// Round thirty-three of the pre-release review.
	if id := identityOnDisk(dir); id != "" {
		return id
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, err := supgang.Status(ctx)
	if err == nil && id.NodeID != "" {
		// Remembered whatever happens, so the NEXT start adopts the fleet
		// identity: that is the documented transition, and it belongs to a
		// restart rather than to whichever process asked first.
		supgang.RememberNodeID(dir, id.NodeID)
		// BUT NOT AHEAD OF A SIBLING THAT ALREADY PUBLISHED ONE. Round
		// forty found the case where this process is the one that waits
		// and mints; the other direction was left open, and it splits the
		// machine the same way: a bridge whose lookup failed fast minted
		// an id and published it while this lookup was still running, and
		// this one then answered with the Supgang id for its whole life.
		// Two identities, one computer, and the hub reads them as two.
		// The minted file is the arbiter for this boot because it is
		// published exclusively (loadOrCreateHostID); the fleet id wins at
		// the next start, where nothing is racing. Round forty-five of the
		// pre-release review.
		if minted := mintedHostID(dir); minted != "" && minted != id.NodeID {
			slog.Debug("another process here published an identity while Supgang was "+
				"answering; keeping it until the next start", "published", minted,
				"supgang", id.NodeID)
			return minted
		}
		return id.NodeID
	}
	// A SILENT SUPGANG IS NOT AN ABSENT ONE, and another process here may
	// be in the middle of the same question.
	//
	// Two bridges starting together against a fresh directory on a member
	// both find nothing recorded. If one lookup answers and the other
	// times out, the first records the fleet id and the second mints a
	// random one and keeps it for its life: one computer with two
	// identities, which is the split all of this exists to prevent, and
	// the exclusive publication of host_id does not help because the two
	// take different branches. So a process that was refused an answer,
	// on a machine where Supgang exists, waits briefly for the answer
	// somebody else got before minting anything. Round forty of the
	// pre-release review.
	if !errors.Is(err, supgang.ErrNotInstalled) {
		if recorded := waitForRecordedIdentity(dir); recorded != "" {
			slog.Debug("Supgang did not answer this process; another one here recorded "+
				"this machine's identity meanwhile", "node", recorded)
			return recorded
		}
	}
	slog.Debug("no recorded identity for this machine and Supgang did not name it; "+
		"minting one for this data directory", "err", err)
	return loadOrCreateHostID(dir)
}

// mintedHostID is the id a bridge minted for this data directory, and
// nothing else: not the daemon's node id, which every member has beside
// its Supgang identity and which must never outrank it.
func mintedHostID(dir string) string {
	if dir == "" {
		return ""
	}
	// #nosec G304 -- the user's own data directory
	b, err := os.ReadFile(filepath.Join(dir, "host_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// identityOnDisk is what this data directory says this machine is: the id
// Supgang gave here, else the daemon's own node id or a host id minted
// here earlier. "" when the directory has never named it.
func identityOnDisk(dir string) string {
	if remembered := supgang.RememberedNodeID(dir); remembered != "" {
		return remembered
	}
	return recordedHostID(dir)
}

// identityWait is how long a process whose own Supgang lookup failed
// gives a sibling to record the answer, polled every identityPoll.
// Variables so a test can shorten them.
var (
	identityWait = 2 * time.Second
	identityPoll = 100 * time.Millisecond
)

func waitForRecordedIdentity(dir string) string {
	deadline := time.Now().Add(identityWait)
	for {
		if id := identityOnDisk(dir); id != "" {
			return id
		}
		if !time.Now().Before(deadline) {
			return ""
		}
		time.Sleep(identityPoll)
	}
}

var (
	hostIDOnce  sync.Once
	hostIDValue string
)

// recordedHostID is the identity this data directory already holds: the
// daemon's own node id, else a host id minted here earlier. "" when the
// directory has never named this machine, which is the one case Supgang is
// asked about.
func recordedHostID(dir string) string {
	if dir == "" {
		return ""
	}
	for _, name := range []string{"node_id", "host_id"} {
		// #nosec G304 -- the user's own data directory
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id
			}
		}
	}
	return ""
}

// resolvedHostFile is where the answer above is published for the one
// reader that cannot compute it: the opencode plugin, which runs no
// subprocess and so cannot ask Supgang. It reads this file first, so the
// host it stamps on a guard is the host the bridge stamped on the
// registration, whichever source the bridge took it from. Without this the
// plugin read node_id, the bridge answered with Supgang's id on a member,
// and the daemon's host-scoped guard resolved the two to different
// machines: the guard e2e caught it on the first machine with Supgang.
const resolvedHostFile = "resolved_host_id"

// resolvedOriginFile is where the bridge publishes the origin it dialled,
// after any DIBS_BOARD_PEER resolution, for the same reader: the opencode
// plugin derived its endpoint from the saved DIBS_ADDR alone, so after a
// Supgang hub moved, the bridge beside it reconnected and the plugin went on
// dialling the old address, losing delivery and failing its guard open.
// Round twenty-six of the pre-release review.
const resolvedOriginFile = "resolved_origin"

// publishResolvedHostID writes the resolved id beside the secret.
func publishResolvedHostID(dir, id string) { publishResolved(dir, resolvedHostFile, id) }

// publishResolvedOrigin writes the origin the bridge dialled beside the secret.
func publishResolvedOrigin(dir, origin string) { publishResolved(dir, resolvedOriginFile, origin) }

// publishResolved writes one resolved value beside the secret, whole and
// only when it changed: a torn read would be a wrong answer, which is worse
// than none, so the bytes land under another name and are renamed into
// place.
func publishResolved(dir, name, value string) {
	if dir == "" || value == "" {
		return
	}
	path := filepath.Join(dir, name)
	// #nosec G304 -- the user's own data directory
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) == value {
		return
	}
	sum := sha256.Sum256([]byte(value))
	tmp := path + ".new-" + hex.EncodeToString(sum[:8])
	if err := os.WriteFile(tmp, []byte(value+"\n"), 0o600); err != nil {
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
	// WHATEVER THE DIRECTORY SAYS NOW, which includes an id Supgang gave
	// another process here since this one decided to mint: the caller read
	// the same files a moment ago, and a moment is the whole window.
	// Round forty of the pre-release review.
	if id := identityOnDisk(dir); id != "" {
		return id
	}
	path := filepath.Join(dir, "host_id")
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
