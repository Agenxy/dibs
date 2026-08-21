package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A plugin must not advertise a delivery moment its own hooks do not bind.
//
// The claude-code entry said "a SessionStart hook and a PreToolUse hook call
// the wake path for you, so a question addressed to your agent appears in your
// context on your next tool call". PreToolUse binds the claim guard and nothing
// else, so mail never arrived on a tool call: it arrives at a turn boundary,
// and an agent working a long autonomous stretch has no boundary until it
// finishes.
//
// The cost was measured, not theorised. A peer sent a question with the default
// 600-second deadline to an agent seven hours into such a stretch, was told the
// recipient was dormant, and reported that Dibs does not deliver messages.
// `buys` is read by an agent deciding whether it still needs to poll, so a
// delivery claim that overstates is the one that loses mail.
func TestNoPluginAdvertisesAWakeEventItDoesNotBind(t *testing.T) {
	// Every Claude Code lifecycle event, so a claim naming one can be checked
	// against what the plugin actually registers for it.
	events := []string{
		"SessionStart", "UserPromptSubmit", "Stop", "SubagentStop",
		"PreToolUse", "PostToolUse", "Notification", "PreCompact", "SessionEnd",
	}

	for _, p := range catalog {
		hooksPath := filepath.Join("..", "..", "plugins", p.dir, "hooks", "hooks.json")
		raw, err := os.ReadFile(hooksPath) // #nosec G304 -- a path in this repository
		if err != nil {
			continue // not every plugin ships hooks
		}
		var doc struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Tool string `json:"tool"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", hooksPath, err)
		}

		wakes := map[string]bool{}
		for ev, groups := range doc.Hooks {
			for _, g := range groups {
				for _, h := range g.Hooks {
					if h.Tool == "hook_poll" {
						wakes[ev] = true
					}
				}
			}
		}
		if len(wakes) == 0 {
			t.Fatalf("%s binds hook_poll to nothing, so this probe cannot see a "+
				"delivery path at all", hooksPath)
		}

		for _, ev := range events {
			if wakes[ev] || !strings.Contains(p.buys, ev) {
				continue
			}
			t.Errorf("%s advertises %s as a delivery moment, and %s binds hook_poll "+
				"only to %v. An agent reading that stops polling and loses mail",
				p.harness, ev, hooksPath, keys(wakes))
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
