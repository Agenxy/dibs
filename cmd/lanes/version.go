package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// printVersion reports this CLI's build AND the daemon's, and says when they
// disagree.
//
// `lanes version` used to print one line about itself, which is the least useful
// half of the answer. `lanes` and `lanesd` are separate processes: installing a
// new binary does not restart the one already serving, so a fix can be built,
// installed, and completely absent from every answer the board gives — with no
// error anywhere, and both sides reporting the same version string, because in
// development they are both `0.0.0-dev`.
//
// That is not hypothetical. A daemon predating a panel fix kept serving the old
// template for hours while the repository, the tests, and the freshly installed
// binary all agreed the fix was in; the only visible symptom was a screenshot
// that looked wrong. The question people actually have is "is what is running
// what I last built", and a version string cannot answer it. A timestamp can.
func printVersion() {
	fmt.Println("lanes", version)

	info, err := daemonBuild()
	if err != nil {
		// Not an error. No daemon is a perfectly ordinary state, and a version
		// command that fails because nothing is listening would be worse than one
		// that says less.
		fmt.Println("lanesd  not running (nothing listening on " + addr() + ")")
		return
	}

	if info.StartedAt == "" {
		// A daemon that cannot say when it started predates this field, which
		// answers the question by itself: it is older than the binary that is
		// asking. Saying so beats printing "started" followed by nothing.
		fmt.Printf("lanesd  %s  (running a build older than this CLI — it does not\n", info.Version)
		fmt.Println("        report its start time, which was added alongside this check)")
		fmt.Println()
		fmt.Println("  restart it to pick up the installed build:")
		fmt.Println()
		fmt.Println("      pkill lanesd && lanesd &")
		return
	}
	fmt.Printf("lanesd  %s  started %s\n", info.Version, humanAge(info.StartedAt))
	if info.PanelBuild != "" {
		fmt.Println("panel  ", info.PanelBuild)
	}

	// The comparison that matters: is the binary on disk newer than the process?
	self := info.BinaryPath
	if self == "" {
		return
	}
	st, serr := os.Stat(self)
	if serr != nil {
		return
	}
	if staleDaemon(st.ModTime(), info.StartedAt) {
		fmt.Println()
		fmt.Printf("  the daemon is STALE: %s was rebuilt %s, after the running\n",
			self, humanAge(st.ModTime().UTC().Format(time.RFC3339)))
		fmt.Println("  process started. It is still serving the older code, and nothing")
		fmt.Println("  else will say so — restart it to pick the new build up:")
		fmt.Println()
		fmt.Println("      pkill lanesd && lanesd &")
	}
}

type buildInfo struct {
	Version    string `json:"version"`
	StartedAt  string `json:"started_at"`
	PanelBuild string `json:"panel_build"`
	BinaryPath string `json:"binary_path"`
}

// daemonBuild asks the daemon what it is running, over the handshake it already
// answers. No token: this is the same information it gives every client on
// connect, and requiring a lane token to ask "what version are you" would make
// the diagnostic unavailable in exactly the confused state it exists for.
func daemonBuild() (buildInfo, error) {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "server/discover", "params": map[string]any{},
	})
	req, err := http.NewRequest(http.MethodPost, "http://"+addr()+"/mcp", bytes.NewReader(body))
	if err != nil {
		return buildInfo{}, err
	}
	req.Header.Set("content-type", "application/json")
	if s, serr := localSecret(); serr == nil {
		req.Header.Set("X-Lanes-Local", s)
	}
	res, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return buildInfo{}, err
	}
	defer func() { _ = res.Body.Close() }()
	var out struct {
		Result struct {
			ServerInfo buildInfo `json:"serverInfo"`
		} `json:"result"`
	}
	if derr := json.NewDecoder(res.Body).Decode(&out); derr != nil {
		return buildInfo{}, derr
	}
	if out.Result.ServerInfo.Version == "" {
		return buildInfo{}, fmt.Errorf("no serverInfo in the daemon's answer")
	}
	return out.Result.ServerInfo, nil
}

// humanAge renders an RFC3339 stamp the way the board renders ages.
func humanAge(stamp string) string {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return stamp
	}
	return ago(t)
}

// staleDaemon reports whether the binary on disk was built after the process
// serving it started.
//
// Extracted so the decision can be tested without a daemon, a filesystem or a
// clock. Inline it was three lines nothing could reach, which in this codebase
// is the shape every guard has taken shortly before turning out not to work.
//
// Unparseable means NOT stale. A false alarm here sends somebody to restart a
// daemon that was fine, and teaches them to ignore the line — which costs more
// than the silence, because the one time it is right is the time they need it.
func staleDaemon(binaryMtime time.Time, startedAt string) bool {
	started, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return false
	}
	return binaryMtime.After(started)
}
