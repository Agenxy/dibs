// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import (
	"encoding/json"
	"testing"
)

// codexRequiresApproval is Codex's decision, transcribed from
// codex-rs/core/src/mcp_tool_call.rs requires_mcp_tool_approval at 3df96b085,
// over the hints as a client decodes them from tools/list: nil means absent.
func codexRequiresApproval(readOnly, destructive, openWorld *bool) bool {
	if destructive != nil && *destructive {
		return true
	}
	if readOnly != nil && *readOnly {
		return false
	}
	return destructive == nil || *destructive || openWorld == nil || *openWorld
}

type advertisedHints struct {
	ReadOnly    *bool `json:"readOnlyHint"`
	Destructive *bool `json:"destructiveHint"`
	OpenWorld   *bool `json:"openWorldHint"`
}

// What a client actually reads: the tools/list result, through JSON.
func advertisedAnnotations(t *testing.T) map[string]*advertisedHints {
	t.Helper()
	raw, err := json.Marshal(agentTools)
	if err != nil {
		t.Fatal(err)
	}
	var tools []struct {
		Name        string           `json:"name"`
		Annotations *advertisedHints `json:"annotations"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	out := map[string]*advertisedHints{}
	for _, tool := range tools {
		out[tool.Name] = tool.Annotations
	}
	return out
}

// A seat cut off from its board by a harness restart: agenxy-dev-2's thread
// came back sandboxed under approval policy "never", and Codex refused its
// register because Dibs declared no hints, which a client reads as
// "destructive, open-world". The calls a seat needs to come back and
// coordinate must never need a person's approval.
func TestASeatCanReattachAndCoordinateWithoutApproval(t *testing.T) {
	hints := advertisedAnnotations(t)
	for _, name := range []string{
		"register", "resume", "check_in", "heartbeat", "inbox", "read_mail", "respond", "ack",
		"send", "declare", "undeclare", "claim", "release", "board", "await_events",
	} {
		h := hints[name]
		if h == nil {
			t.Errorf("%s advertises no annotations, so Codex treats it as destructive and open-world", name)
			continue
		}
		if codexRequiresApproval(h.ReadOnly, h.Destructive, h.OpenWorld) {
			t.Errorf("%s would need approval under Codex's rule; a sandboxed thread with policy never "+
				"is refused it", name)
		}
	}
}

// Every tool is classified, and what it advertises, read with MCP's defaults
// for the hints it leaves out, is that classification: a new tool cannot ship
// meaning "destructive, open-world" by omission. The lists name only real
// tools, because a misspelt entry would classify nothing and say nothing.
func TestEveryToolDeclaresItsAnnotations(t *testing.T) {
	names := map[string]bool{}
	for _, def := range toolDefs {
		name, _ := def["name"].(string)
		names[name] = true
		a, ok := def["annotations"].(map[string]any)
		if !ok {
			t.Errorf("%s has no annotations", name)
			continue
		}
		hint := func(key string, def bool) bool {
			if v, ok := a[key].(bool); ok {
				return v
			}
			return def
		}
		if hint("openWorldHint", true) {
			t.Errorf("%s reads as open-world; Dibs is a closed board", name)
		}
		readOnly := hint("readOnlyHint", false)
		if readOnly != readOnlyTools[name] {
			t.Errorf("%s reads as readOnly=%v, classified %v", name, readOnly, readOnlyTools[name])
		}
		if !readOnly && hint("destructiveHint", true) != destructiveTools[name] {
			t.Errorf("%s reads as destructive=%v, classified %v", name, hint("destructiveHint", true),
				destructiveTools[name])
		}
	}
	for _, list := range []map[string]bool{readOnlyTools, destructiveTools} {
		for name := range list {
			if !names[name] {
				t.Errorf("%q is classified but is not a tool", name)
			}
		}
	}
}

// The destructive ones stay marked: a client should ask before an agent is
// evicted or one row is folded into another, and a thread that cannot ask
// is right to refuse.
func TestToolsThatOverrideOthersStayDestructive(t *testing.T) {
	hints := advertisedAnnotations(t)
	for name := range destructiveTools {
		h := hints[name]
		if h == nil {
			continue // not advertised to models (harness-only); still annotated, checked above
		}
		if !codexRequiresApproval(h.ReadOnly, h.Destructive, h.OpenWorld) {
			t.Errorf("%s overrides another agent's state and must still need approval", name)
		}
	}
}
