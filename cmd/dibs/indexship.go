package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/overlap"
	"github.com/agenxy/dibs/internal/paths"
)

// shipIndexOnRegister makes the bridge the side that reads the checkout when
// the daemon cannot (issue #19).
//
// Matching mines the repository, so the daemon needed read access to every
// tree its agents work in, and on macOS a daemon started by launchd is not
// granted ~/Desktop, ~/Documents or ~/Downloads: /usr/bin/git blocks there on
// a prompt no background process can show. This bridge runs INSIDE the
// checkout, as a child of the harness, with the access the person already
// granted the harness. So after a registration it asks the daemon whether it
// could read this tree and, only when the daemon says it could not, mines the
// two bounded things the index is built from (tracked paths, commit subjects
// with the files each touched; never contents) and ships them.
//
// Only when asked, because a daemon that can read the tree already has a
// better index than a copy, and shipping ~400 KB on every session start for
// it to be discarded would be work for nothing. The daemon's verdict is
// asynchronous (it indexes on the registration's goroutine), so this asks
// shortly after registering and once more later, then stops: a tree the
// daemon indexed in between needs nothing from here.
func shipIndexOnRegister(
	ctx context.Context, client *http.Client, url, secret string, timing shipTiming, next func(sent, reply []byte),
) func(sent, reply []byte) {
	var (
		mu       sync.Mutex
		watching = map[string]*shipper{} // one shipper per root, for the life of the bridge
	)
	return func(sent, reply []byte) {
		next(sent, reply)
		// THE DIRECTORY THE AGENT REGISTERED, not the one this bridge runs in.
		// register takes a cwd and update can correct it, and the daemon's
		// verdict is about that tree; a bridge started outside the checkout
		// watched its own directory and the verdict for the real one never
		// triggered a shipment. Round seven of the pre-release review.
		var tok, cwd string
		switch toolNameOf(sent) {
		case "register", "resume":
			tok, cwd = agentTokenIn(reply), argIn(sent, "cwd")
		case "update":
			tok, cwd = argIn(sent, "token"), argIn(sent, "cwd")
			if cwd == "" {
				return // an update that did not move the agent
			}
		default:
			return
		}
		if tok == "" {
			return
		}
		if cwd == "" {
			wd, err := os.Getwd()
			if err != nil {
				return
			}
			cwd = wd
		}
		root := repoRootOf(filepath.Clean(cwd))
		if root == "" {
			return // not a checkout; nothing to index anywhere
		}
		mu.Lock()
		sh := watching[root]
		if sh == nil {
			sh = &shipper{token: tok}
			watching[root] = sh
			go shipWhenUnreadable(ctx, client, url, secret, sh, root, timing)
		} else {
			// THE CREDENTIAL ROTATES: a resume, or a register that reattached,
			// returns a fresh token and revokes the one the shipper captured.
			// A shipment with the old one is a 401 from the daemon and no
			// index for that tree, however many times the agent re-registers.
			// Round eight of the pre-release review.
			sh.set(tok)
		}
		mu.Unlock()
	}
}

// shipper is one tree's shipment loop and the credential it ships with, which
// the hook replaces on every register or resume that hands out a new one.
type shipper struct {
	mu    sync.Mutex
	token string
}

func (s *shipper) set(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = tok
}

func (s *shipper) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// argIn reads one string argument of a tools/call, or "".
func argIn(sent []byte, key string) string {
	var m struct {
		Params struct {
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(sent, &m) != nil {
		return ""
	}
	v, _ := m.Params.Arguments[key].(string)
	return v
}

// shipSchedule is how long to wait between each look at the daemon's verdict.
//
// It has to outlast the daemon's own patience with Git. The daemon gives a
// git call four minutes (cmd/dibd gitDeadline), because on macOS a blocked
// read is a permission dialog waiting for a person, and only after that does
// it mark the tree unreadable. This used to look at three and forty-eight
// seconds and stop, so for the documented TCC hang both looks saw "indexing",
// nothing shipped, and the fallback this exists for never activated. Found
// by the pre-release review. Bounded, cheap (one GET each), and it stops the
// moment there is something to ship.
var shipSchedule = []time.Duration{
	3 * time.Second, 45 * time.Second,
	time.Minute, time.Minute, time.Minute, time.Minute, time.Minute,
}

// shipTiming is when a shipper looks at the verdict: the bounded schedule
// after registration, then the slow recheck for the life of the bridge. A
// value handed to the shipper rather than globals it reads, so a test can
// shorten it without writing under a goroutine that is reading it.
type shipTiming struct {
	schedule []time.Duration
	recheck  time.Duration
}

// defaultShipTiming is what the bridge ships on.
func defaultShipTiming() shipTiming {
	return shipTiming{schedule: shipSchedule, recheck: shipRecheckEvery}
}

// daemonGitDeadline mirrors cmd/dibd's gitDeadline, which this package cannot
// import; the test beside this holds the schedule to it.
const daemonGitDeadline = 4 * time.Minute

// shipRecheckEvery is how often, after the schedule above has run out, the
// bridge keeps looking at the daemon's verdict for as long as it lives.
//
// A supplied index is held in the daemon's memory, and a daemon restart
// (`dibs upgrade`, a reboot) loses it, while the bridge and its token go on
// working: the schedule had run out on registration, nothing asked again,
// and matching stayed unavailable for that tree until another registration
// happened to ship. One GET every few minutes per bridge is the cost of a
// verdict that can change. Round five of the pre-release review.
const shipRecheckEvery = 5 * time.Minute

// shipWhenUnreadable watches the daemon's verdict on the schedule above and
// then at shipRecheckEvery for the life of the bridge, ships whenever the
// daemon wants an index this bridge has not supplied, and says nothing on
// success: the daemon logs what it installed.
func shipWhenUnreadable(
	ctx context.Context, client *http.Client, url, secret string, sh *shipper, root string, timing shipTiming,
) {
	for i := 0; ; i++ {
		wait := timing.recheck
		if i < len(timing.schedule) {
			wait = timing.schedule[i]
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		// The daemon this bridge REGISTERED with, at the address it reached
		// it through. fetchMatchStatus builds its own from origin(), which is
		// the configured address before Supgang resolved a moved hub, so the
		// shipment went one way and every verdict poll the other. Round three
		// of the pre-release review.
		st := fetchMatchStatusAt(client, apiBase(url), secret)
		if !wantsIndex(st, root) || suppliedFor(st, root) {
			continue
		}
		if err := shipIndex(ctx, client, url, secret, sh.get(), root); err != nil {
			fmt.Fprintln(os.Stderr, "dibs: could not ship the index for", root+":", err)
		}
	}
}

// suppliedFor reports whether the daemon already holds a shipped index for
// this root: after one shipment the verdict still lists the tree (it is
// still one the daemon cannot read), and Supplied is what says it is served.
func suppliedFor(st matchStatusJSON, root string) bool {
	_, ok := st.Supplied[root]
	return ok
}

// wantsIndex reads the daemon's verdict: it tried this tree and could not
// read it. A tree it indexed, is indexing, or has not looked at yet is not
// one to ship for.
func wantsIndex(st matchStatusJSON, root string) bool {
	for _, tree := range st.Unreadable {
		if tree == root || underDir(tree, root) {
			return true
		}
	}
	// A tree on another machine: unreadable here by definition, and never
	// tried, so it is listed apart. This bridge is on that machine when the
	// root matches. Round four of the pre-release review.
	for _, tree := range st.Remote {
		if tree == root || underDir(tree, root) {
			return true
		}
	}
	return false
}

func shipIndex(ctx context.Context, client *http.Client, url, secret, token, root string) error {
	mctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	payload, err := overlap.Ship(mctx, root, overlap.DefaultCoChangeOptions)
	if err != nil {
		return err
	}
	// The project's identity as this side reads it, so the daemon can pair
	// this index with another clone's. Its word, and used only for that.
	payload.RepoDir, payload.RepoRemote, payload.RepoRoots, _ = paths.Identify(root).Identity()
	body, err := json.Marshal(struct {
		Token string `json:"token"`
		*overlap.Payload
	}{token, payload})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(mctx, http.MethodPost, indexURL(url), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Accepted bool   `json:"accepted"`
		Error    string `json:"error"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("daemon answered %d with no readable verdict: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || !out.Accepted {
		why := out.Error
		if why == "" {
			why = out.Reason
		}
		return fmt.Errorf("daemon did not take it (%d): %s", resp.StatusCode, why)
	}
	return nil
}

// indexURL is the /api/index beside the /mcp the bridge already talks to.
func indexURL(mcpURL string) string {
	return apiBase(mcpURL) + "/api/index"
}

// apiBase is the daemon's origin as reached through its MCP URL.
func apiBase(mcpURL string) string {
	if len(mcpURL) >= 4 && mcpURL[len(mcpURL)-4:] == "/mcp" {
		return mcpURL[:len(mcpURL)-4]
	}
	return mcpURL
}
