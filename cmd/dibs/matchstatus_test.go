package main

import (
	"strings"
	"testing"
)

// A shipped index serves the subdirectories of the tree it was shipped
// for, and doctor says so.
//
// A remote entry is a directory an AGENT is in; a shipment names the
// repository ROOT. Comparing them as strings told the operator that a
// working bridge had never shipped and sent them to troubleshoot it:
// the agent in /repo/pkg is served by the index for /repo. Round
// fifty-two of the pre-release review.
func TestASuppliedIndexCoversTheSubdirectoriesOfItsTree(t *testing.T) {
	st := matchStatusJSON{
		Remote:        []string{"/repo/pkg"},
		RemoteHosts:   map[string]string{"/repo/pkg": "machine-b"},
		Supplied:      map[string]string{"/repo": "shipper"},
		SuppliedHosts: map[string]string{"/repo": "machine-b"},
		Host:          "hub-node",
	}
	var said []string
	reportRemoteTrees(st, func(msg, _ string) { said = append(said, msg) })
	for _, m := range said {
		if strings.Contains(m, "index has not arrived") {
			t.Fatalf("doctor says the index never arrived for an agent in a subdirectory "+
				"of a tree that was shipped: %q", m)
		}
	}

	// A tree nothing covers is still reported, which is the point of the
	// check.
	st.Remote = append(st.Remote, "/other")
	st.RemoteHosts["/other"] = "machine-c"
	said = nil
	reportRemoteTrees(st, func(msg, _ string) { said = append(said, msg) })
	found := false
	for _, m := range said {
		if strings.Contains(m, "/other") && strings.Contains(m, "index has not arrived") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a remote tree with no index at all was not reported: %v", said)
	}

	// And the wrong machine's index at the covering root is still called
	// out, asked of the root that serves the tree.
	st.Remote = []string{"/repo/pkg"}
	st.SuppliedHosts["/repo"] = "machine-z"
	said = nil
	reportRemoteTrees(st, func(msg, _ string) { said = append(said, msg) })
	found = false
	for _, m := range said {
		if strings.Contains(m, "shipped from a different one") {
			found = true
		}
	}
	if !found {
		t.Fatalf("an index shipped from another machine at that path was not reported: %v", said)
	}
}
