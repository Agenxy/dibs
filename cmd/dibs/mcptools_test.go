package main

import (
	"slices"
	"sort"
	"testing"

	"github.com/agenxy/dibs/internal/mcp"
)

// mcpTools is a copy of the MCP listing, kept so the CLI can tell a human that
// `dibs prune` is a real verb on the other surface rather than a typo.
//
// A copy is a liability the moment it drifts: a tool added later would be
// offered the nearest unrelated CLI verb again, which is the exact confusion
// this exists to remove, and nothing would say so. skills.md and the embedded
// plugins have the same arrangement and the same guard.
//
// This lives in a _test.go file on purpose. The import is what holds the copy
// honest, and a test-only import puts none of internal/mcp in the shipped CLI,
// which is why the copy exists at all.
func TestMCPToolListMatchesTheServer(t *testing.T) {
	want := mcp.ToolNames()
	for _, c := range commands {
		want = slices.DeleteFunc(want, func(n string) bool { return n == c })
	}
	sort.Strings(want)

	got := slices.Clone(mcpTools)
	sort.Strings(got)

	if !slices.Equal(got, want) {
		var missing, extra []string
		for _, n := range want {
			if !slices.Contains(got, n) {
				missing = append(missing, n)
			}
		}
		for _, n := range got {
			if !slices.Contains(want, n) {
				extra = append(extra, n)
			}
		}
		t.Errorf("the CLI's copy of the MCP tool names has drifted from the server.\n"+
			"missing (a human typing these is told the verb does not exist): %v\n"+
			"stale (no longer tools): %v", missing, extra)
	}
}

// A name on BOTH surfaces must not be claimed by the MCP-only branch, or the
// CLI would refuse to run its own verb.
func TestSharedNamesAreNotReportedAsMCPOnly(t *testing.T) {
	for _, c := range commands {
		if mcpOnlyTool(c) {
			t.Errorf("%q is a CLI verb but is listed as MCP-only: `dibs %s` would be "+
				"told to call it over MCP instead of running", c, c)
		}
	}
}
