package mcp

import (
	"encoding/json"

	"github.com/agenxy/lanes/internal/core"
)

// panelPayload trims board/mailbox state down to exactly the fields the panel
// template reads.
//
// This is not premature tidying. The payload travels in tool-result _meta, the
// MCP App's private backchannel, so it does not enter model context. It is still
// sent on every panel-bearing call, however, and unbounded invisible transport
// is still waste: send only what the template draws.
func panelPayload(raw core.Result) core.Result {
	// Normalise to plain JSON types FIRST.
	//
	// core.Result is a named map type and the engine returns typed slices
	// ([]core.Event, []core.Message). Neither satisfies `case map[string]any` or
	// `.([]any)` in a type switch. Go treats a named type as distinct. That has
	// silently produced a wrong answer three times in this file's history: a
	// summary reporting 0 lanes over 7, a board dropped entirely, and a mailbox
	// panel suppressed while holding mail. One round-trip removes the whole class.
	in := core.Result{}
	if b, err := json.Marshal(raw); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			for k, v := range m {
				in[k] = v
			}
		}
	}
	out := core.Result{}
	for _, k := range []string{"view", "lane_id", "act_token"} {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	// core.Result is a named map type, so a board stored as a plain
	// map[string]any does NOT satisfy a core.Result assertion. Both shapes occur
	// depending on whether the value came straight from the engine or through a
	// JSON round-trip: accept either, or the board silently vanishes.
	if b := asMap(in["board"]); b != nil {
		out["board"] = trimBoard(b)
	}
	if msgs := extractMessages(in["inbox"]); msgs != nil {
		out["inbox"] = msgs
	}
	// await_events returns BECAUSE something changed; the panel shows what.
	// Cap the activity list. A cursor reaching back far enough returns the whole
	// history (261 events in one observed call) which is neither readable nor
	// worth the payload. The newest are the ones the human is looking for.
	all := asMaps(in["events"])
	if n := len(all); n > maxPanelEvents {
		all = all[n-maxPanelEvents:]
	}
	var evs []map[string]any
	for _, e := range all {
		evs = append(evs, pick(e, eventFields))
	}
	if evs != nil {
		out["events"] = evs
	}
	return out
}

// laneFields / slotFields / msgFields are what board_app.html actually renders.
// Adding a field here without using it in the template is how payloads rot.
var (
	laneFields = []string{
		"id", "name", "kind", "status", "description", "last_coordination_at", "agent",
		// WHY an agent stopped counting as live. Without it the panel shows
		// "out of touch" beside a last-contact time of "now", which reads as a
		// broken panel rather than a dead agent, and it cannot tell a crashed
		// process from a lane that never gave a pid and is simply quiet.
		"stale_reason",
		// The name a human chose, when the id could not carry it. Without it a
		// fleet named in a non-Latin script reads `lane`, `lane-2`, `lane-3`.
		"display_name",
	}
	slotFields = []string{"id", "text", "refs", "dirs"}
)

// channelFields / memberFields are what the Lanes tab renders. Same discipline
// as laneFields: a field added here and not drawn is payload rot.
var (
	channelFields = []string{
		"id", "topic", "owner", "queue",
		"unacked_announcements", "abandoned_announcements", "blocked_announcements",
		"departed_unacked",
	}
	memberFields = []string{"agent", "auto", "score", "threshold", "scorer", "evidence"}
)

// `state` is load-bearing, not decoration. `response` is a plain STRING: the
// disposition an agent chose (approved / denied / declined / acked) lives only
// in state, so without it the panel can show that a request was answered but
// never whether it was granted. That is the one thing the reader needs.
var (
	// `attachments` is here because the panel dropped it: a message carrying a
	// blob rendered identically to one carrying nothing, so "review the attached
	// evidence" showed no attachment at all. The shared renderer displays them.
	msgFields   = []string{"serial", "type", "from", "to", "body", "response", "state", "attachments"}
	eventFields = []string{"serial", "type", "lane", "to", "ts"}
)

// maxPanelEvents bounds the activity list: enough to see what just happened,
// not so many that the panel becomes a log file.
const maxPanelEvents = 40

func trimBoard(b map[string]any) core.Result {
	out := core.Result{}
	for _, k := range []string{"node", "serial"} {
		if v, ok := b[k]; ok {
			out[k] = v
		}
	}
	var lanes []map[string]any
	for _, raw := range asMaps(b["lanes"]) {
		l := pick(raw, laneFields)
		var slots []map[string]any
		for _, s := range asMaps(raw["slots"]) {
			slots = append(slots, pick(s, slotFields))
		}
		if slots != nil {
			l["slots"] = slots
		}
		lanes = append(lanes, l)
	}
	out["lanes"] = lanes

	// Channels, trimmed the same way and for the same reason: this payload is
	// sent to every host on every board call, so anything the template does not
	// draw is context the model pays for and nobody reads. Membership carries
	// its score and evidence because SPEC-CHANNELS.md §10.3 requires an
	// auto-join to be explainable, and the panel renders exactly that in the
	// member's title attribute.
	var chans []map[string]any
	for _, raw := range asMaps(b["channels"]) {
		c := pick(raw, channelFields)
		var members []map[string]any
		for _, m := range asMaps(raw["members"]) {
			members = append(members, pick(m, memberFields))
		}
		if members != nil {
			c["members"] = members
		}
		chans = append(chans, c)
	}
	if chans != nil {
		out["channels"] = chans
	}
	return out
}

func extractMessages(box any) []map[string]any {
	var raw any
	if m := asMap(box); m != nil {
		raw = m["messages"]
	} else {
		raw = box
	}
	var out []map[string]any
	for _, m := range asMaps(raw) {
		out = append(out, pick(m, msgFields))
	}
	return out
}

// asMap accepts either the named type or a bare map.
func asMap(v any) map[string]any {
	switch m := v.(type) {
	case core.Result:
		return m
	case map[string]any:
		return m
	}
	return nil
}

func pick(m map[string]any, keys []string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && v != "" {
			out[k] = v
		}
	}
	return out
}

// asMaps normalises the several shapes a slice of records arrives in: typed
// slices from core, []any after a JSON round-trip: without reflection.
func asMaps(v any) []map[string]any {
	switch s := v.(type) {
	case []map[string]any:
		return s
	case []core.Result:
		out := make([]map[string]any, 0, len(s))
		for _, r := range s {
			out = append(out, r)
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(s))
		for _, e := range s {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}
