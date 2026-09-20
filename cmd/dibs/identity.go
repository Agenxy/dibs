package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

// identityCmd prints what this machine and checkout are, as the stdio bridge
// would stamp them: for an integration that speaks MCP itself and so does
// not pass through the bridge.
//
// The pi extension is one. It re-implemented the bridge's answers in
// TypeScript one at a time, as each review round found the next one it
// lacked (the host, canonical paths, the trust store), and the fourth was
// the repository identity: without `com.dibs/repo` a remote pi agent
// registered with no checkout, and two clones of one repository on two
// machines were both granted an exclusive claim on the same tracked file.
// The answers already exist in this binary and change together; a second
// implementation of them drifts. So they are printed, once per process, and
// read by whoever needs them. Round twenty-two of the pre-release review.
//
// Output is one JSON object: `host_id` (hostID, which also publishes it for
// the plugins that read files), `cwd` as the bridge would register it,
// `repo` (what repoMeta sends, or null outside a checkout), and `origin`,
// where the daemon is now (boardOrigin).
func identityCmd(args []string) error {
	fs := flag.NewFlagSet("identity", flag.ContinueOnError)
	cwd := fs.String("cwd", "", "the working directory to describe (default: this process's)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := *cwd
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("no working directory: %w", err)
		}
		dir = wd
	}
	params := map[string]any{"name": "register", "arguments": map[string]any{"cwd": dir}}
	canonicalisePathArgs(params)
	out := map[string]any{
		"host_id": hostID(),
		"cwd":     params["arguments"].(map[string]any)["cwd"],
		// Where the daemon is NOW: the saved address, re-pointed at the hub's
		// current Supgang address when the config names it as a peer, as the
		// bridge dials (boardOrigin). An integration that derived its endpoint
		// from DIBS_ADDR alone kept dialling a hub that had moved. Round
		// twenty-six of the pre-release review.
		"origin": identityOrigin(),
		"repo":   nil,
	}
	if repo := repoMeta(params); repo != nil {
		out["repo"] = repo
	}
	enc := json.NewEncoder(os.Stdout)
	return enc.Encode(out)
}

// identityOrigin is where the daemon is now, as `dibs identity` reports it;
// a variable so the test binary standing in for dibs can name a server the
// extension under test must follow.
var identityOrigin = boardOrigin
