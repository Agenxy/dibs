package mcp

import (
	"encoding/json"
	"slices"
)

// The board panel opens when somebody asks for it, and a person asks through
// a prompt: hosts list prompts for the user to pick, as a slash command in
// some and a menu entry in others. Agents ask by calling board themselves.
//
// The panel used to ride along on every call that carried board or mailbox
// state, so it appeared on every turn an agent took. The operator asked for it
// to appear only when the agent calls it or the user invokes it, and this is
// the second half of that.
//
// A prompt is text the USER sends, chosen by them, so this is not the board
// putting words in an agent's mouth (AGENTS.md rule 5): nothing reaches a
// conversation unless the person picks it.

// promptViews are the panel views the board prompt can open on, which are
// exactly board's own view enum.
var promptViews = []string{"board", "mail", "activity"}

var promptList = []map[string]any{{
	"name":  "board",
	"title": "Show the Dibs board",
	"description": "Open the Dibs board: every agent, what each is working on, " +
		"your mail and recent activity.",
	"arguments": []map[string]any{{
		"name":        "view",
		"description": "which tab to open on: board, mail or activity (default board)",
		"required":    false,
	}},
}}

func listPrompts() map[string]any {
	return cacheable(map[string]any{"prompts": promptList}, ttlStatic, scopePublic)
}

// getPrompt renders the board prompt as the message the user sends.
func getPrompt(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	_ = json.Unmarshal(params, &p)
	if p.Name != "board" {
		return nil, &rpcError{
			Code: -32602, Message: "unknown prompt " + p.Name,
			Data: hint("call prompts/list: this server has one prompt, board"),
		}
	}
	view := p.Arguments["view"]
	text := "Show me the Dibs board: call the Dibs board tool with your agent token."
	switch {
	case view == "" || view == "board":
	case slices.Contains(promptViews, view):
		text = "Show me the Dibs board on its " + view + " tab: call the Dibs board " +
			"tool with your agent token and view \"" + view + "\"."
	default:
		return nil, &rpcError{
			Code: -32602, Message: "unknown view " + view,
			Data: hint("view is one of board, mail, activity, or leave it out"),
		}
	}
	return map[string]any{
		"description": "Show the Dibs board",
		"messages": []map[string]any{{
			"role":    "user",
			"content": map[string]any{"type": "text", "text": text},
		}},
	}, nil
}
