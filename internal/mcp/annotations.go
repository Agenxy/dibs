// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

// TOOL ANNOTATIONS, and why a missing one cut an agent off.
//
// MCP's ToolAnnotations are hints a client uses to decide what needs a
// person's approval, and every one of them DEFAULTS TO THE ALARMING VALUE:
// a tool that says nothing is taken as destructive and open-world. Codex
// (codex-rs/core/src/mcp_tool_call.rs, requires_mcp_tool_approval, measured
// at 3df96b085 and present in ChatGPT.app's codex 0.162.0-alpha.17.2) asks
// for approval unless a tool is read-only, or declares itself both
// non-destructive and closed-world. Under approval policy "never" in a
// sandboxed thread, "asks" becomes "refuses": "MCP tool call requires
// approval, but approval policy is never".
//
// Dibs declared nothing, so every Dibs tool was a destructive open-world
// call. That held only because most threads run with full disk access,
// which skips the question. On 2026-10-15 a ChatGPT.app restart brought
// agenxy-dev-2's thread back sandboxed, and its register, the call that
// reattaches a seat, was refused: an agent cut off from the board by its
// own harness restarting.
//
// So every tool states what it is, honestly:
//   - openWorldHint is false for all of them. Dibs is a closed board: a tool
//     touches this board's own state and the caller's own files, never an
//     unbounded set of outside entities.
//   - readOnlyHint is true only where the call changes no board state at all.
//     Reading mail is NOT read-only: it marks mail delivered or consumed.
//   - destructiveHint is true where the call removes or overrides state that
//     belongs to somebody else, irreversibly: evicting an agent, breaking a
//     claim, folding one row into another. A client SHOULD ask before those,
//     and a thread that can't ask is right to refuse them.
// TestEveryToolDeclaresItsAnnotations keeps a new tool from shipping without
// a classification, which is how this happened.

// readOnlyTools change no board state.
var readOnlyTools = map[string]bool{
	"board": true, "read_space": true, "events_since": true, "await_events": true,
	"mail_history": true, "all_mail": true, "get_blob": true, "spawned_agents": true,
	"guard_path": true,
}

// destructiveTools remove or override another agent's state irreversibly.
var destructiveTools = map[string]bool{
	"evict": true, "force_release": true, "prune": true, "merge_agents": true,
	"merge_spaces": true, "close_space": true, "relocate": true, "adopt_agent": true,
}

// toolAnnotations states only what differs from MCP's defaults (readOnlyHint
// false, destructiveHint true, openWorldHint true), because every byte here is
// paid by every agent on every cold connection. A hint left out means its
// default, so the classification is still complete: a destructive tool says
// only that it is closed-world, which leaves it destructive.
func toolAnnotations(name string) map[string]any {
	a := map[string]any{"openWorldHint": false}
	switch {
	case readOnlyTools[name]:
		a["readOnlyHint"] = true
	case !destructiveTools[name]:
		a["destructiveHint"] = false
	}
	return a
}

// Annotated where the definitions are built, so the advertised list and the
// full one carry the same answers: agentTools shares these maps.
func init() {
	for _, t := range toolDefs {
		if name, _ := t["name"].(string); name != "" {
			t["annotations"] = toolAnnotations(name)
		}
	}
}
