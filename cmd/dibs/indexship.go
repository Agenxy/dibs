package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	ctx context.Context, client *http.Client, url, secret string, next func(sent, reply []byte),
) func(sent, reply []byte) {
	return func(sent, reply []byte) {
		next(sent, reply)
		if n := toolNameOf(sent); n != "register" && n != "resume" {
			return
		}
		tok := agentTokenIn(reply)
		if tok == "" {
			return
		}
		cwd, err := os.Getwd()
		if err != nil {
			return
		}
		root := repoRootOf(filepath.Clean(cwd))
		if root == "" {
			return // not a checkout; nothing to index anywhere
		}
		go shipWhenUnreadable(ctx, client, url, secret, tok, root)
	}
}

// shipWhenUnreadable asks twice, ships once, and says nothing on success:
// the daemon logs what it installed.
func shipWhenUnreadable(ctx context.Context, client *http.Client, url, secret, token, root string) {
	for _, wait := range []time.Duration{3 * time.Second, 45 * time.Second} {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		st := fetchMatchStatus(client, secret)
		if !wantsIndex(st, root) {
			continue
		}
		if err := shipIndex(ctx, client, url, secret, token, root); err != nil {
			fmt.Fprintln(os.Stderr, "dibs: could not ship the index for", root+":", err)
		}
		return
	}
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
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || (!out.Accepted && out.Error != "") {
		return fmt.Errorf("daemon refused (%d): %s", resp.StatusCode, out.Error)
	}
	return nil
}

// indexURL is the /api/index beside the /mcp the bridge already talks to.
func indexURL(mcpURL string) string {
	if len(mcpURL) >= 4 && mcpURL[len(mcpURL)-4:] == "/mcp" {
		return mcpURL[:len(mcpURL)-4] + "/api/index"
	}
	return mcpURL + "/api/index"
}
