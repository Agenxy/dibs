package main

import (
	"encoding/json"
	"net/http"
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
	req, err := http.NewRequest(http.MethodGet, base+"/api/match-status", nil)
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
