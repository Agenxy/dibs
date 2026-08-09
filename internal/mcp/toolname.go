package mcp

import "encoding/json"

// toolName extracts just the tool name for logging — never the arguments, which
// carry lane tokens and message bodies.
func toolName(params json.RawMessage) string {
	var p struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	return p.Name
}

// resourceURI is the resource a resources/read asked for, for the request log.
//
// Companion to toolName, and it exists for one question: the panel's URI carries
// the build of the template being served, so this line is what says whether a
// host picked up the current panel or is still rendering one it cached. A URI is
// not a credential — unlike a tool call's arguments, which is why that path logs
// only the tool's name.
func resourceURI(params json.RawMessage) string {
	var p struct {
		URI string `json:"uri"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	return p.URI
}
