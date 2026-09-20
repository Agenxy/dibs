package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// The daemon's matching status as `dibs doctor` and the stdio bridge read it.
// Moved out of doctor.go when that file reached the 2000-line limit.

type matchStatusJSON struct {
	Phase   string `json:"phase"`
	Scorer  string `json:"scorer"`
	Files   int    `json:"files"`
	Commits int    `json:"commits"`
	// Repo is which tree those files came from. The daemon has always sent it and
	// this struct dropped it on the floor, so `doctor` reported four thousand
	// indexed files without ever saying whose, which is the one fact that
	// explains a matcher suggesting another project's paths.
	Repo string `json:"repo"`
	Hint string `json:"hint"`
	// Unreadable lists trees the daemon tried and could not read; Supplied
	// maps those an agent shipped the index for instead (issue #19).
	Unreadable []string `json:"unreadable"`
	// Remote lists trees on other machines: the daemon cannot read them by
	// definition, and a bridge on such a machine ships on this list.
	Remote   []string          `json:"remote"`
	Supplied map[string]string `json:"supplied"`
	// Which machine each supplied index and each remote tree belongs to,
	// by root ("" for the daemon's own), and the daemon's own host id: a
	// path repeats across machines, and an index at this path is only
	// served to agents on the machine it was shipped from.
	SuppliedHosts map[string]string `json:"supplied_hosts"`
	RemoteHosts   map[string]string `json:"remote_hosts"`
	Host          string            `json:"host"`
}

// fetchMatchStatus asks the daemon why matching is or is not working. Failure
// to answer is itself an answer: an older daemon has no such endpoint.
func fetchMatchStatus(c *http.Client, secret string) matchStatusJSON {
	return fetchMatchStatusAt(c, origin(), secret)
}

// fetchMatchStatusAt asks the daemon at base, which is the address the caller
// actually reached the daemon through: the bridge's shipment path resolves the
// hub's current address and must poll the same one (indexship.go).
func fetchMatchStatusAt(c *http.Client, base, secret string) matchStatusJSON {
	return fetchMatchStatusCtx(context.Background(), c, base, secret)
}

// fetchMatchStatusCtx is fetchMatchStatusAt bounded by ctx and by its own
// deadline: the bridge's shipment loop asks through its streaming client,
// which has no timeout of its own, and a status request that stalled held
// every later recheck and shipment for that tree, past the loop's own
// cancellation. Round twelve of the pre-release review reproduced it with a
// stalled endpoint.
func fetchMatchStatusCtx(ctx context.Context, c *http.Client, base, secret string) matchStatusJSON {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/match-status", nil)
	if err != nil {
		return matchStatusJSON{}
	}
	req.Header.Set("X-Dibs-Local", secret)
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return matchStatusJSON{}
	}
	defer func() { _ = resp.Body.Close() }()
	var out matchStatusJSON
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// reportRemoteTrees says, for each tree on another machine, whether its
// index has arrived and from where: not a fault of this daemon, which cannot
// read them, and worth a line so "no suggestions for that agent" has a
// stated reason.
func reportRemoteTrees(st matchStatusJSON, warn fixFn) {
	for _, root := range st.Remote {
		if _, shipped := st.Supplied[root]; !shipped {
			warn(root+" is on another machine and its index has not arrived",
				"the bridge on that machine ships it after registering; if it never does, "+
					"run `dibs doctor` there")
			continue
		}
		// Shipped, but by WHICH machine: a path repeats across machines and an
		// index is served only to the one it came from, so another machine's
		// index at this path serves this tree's agent nothing. Round twelve of
		// the pre-release review.
		if by, want := st.SuppliedHosts[root], st.RemoteHosts[root]; want != "" && by != want {
			warn(root+" is on another machine and the index held at that path was "+
				"shipped from a different one", "the daemon holds one tree per path; the "+
				"agent on that machine gets no suggestions until the first machine's "+
				"agents leave and its index is released")
		}
	}
}
