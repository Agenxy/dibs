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

	checked := 0
	for _, p := range catalog {
		// BOTH LAYOUTS, and a miss is not a skip. Claude Code reads
		// plugins/<name>/hooks/hooks.json; Codex reads hooks.json at the root
		// of its config directory. This looked only in the nested place and
		// `continue`d on the read failure, so after Codex's file moved to where
		// Codex actually reads it, deleting its Stop binding left this guard
		// green: the one plugin whose delivery claim had just changed was the
		// one it stopped looking at.
		var raw []byte
		var err error
		hooksPath := ""
		for _, candidate := range []string{
			filepath.Join("..", "..", "plugins", p.dir, "hooks", "hooks.json"),
			filepath.Join("..", "..", "plugins", p.dir, "hooks.json"),
		} {
			if b, rerr := os.ReadFile(candidate); rerr == nil { // #nosec G304 -- a path in this repository
				raw, err, hooksPath = b, nil, candidate
				break
			} else {
				err = rerr
			}
		}
		if raw == nil {
			// Only a plugin that claims no wake may ship no hooks. One that
			// advertises delivery and has no file is the exact overstatement
			// this test exists to catch.
			if strings.Contains(strings.ToLower(p.buys), "wake") ||
				strings.Contains(strings.ToLower(p.buys), "deliver") {
				t.Errorf("%s advertises delivery in `buys` and ships no hooks.json "+
					"in either layout (%v), so nothing binds the events it promises",
					p.dir, err)
			}
			continue
		}
		checked++
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

		// EVERY published string, not just the summary.
		//
		// The first version of this read `buys` alone, so the same false claim
		// survived in the Setup steps beside it: "it appears in your context on
		// your next tool call", with a failure hint sending the operator to hunt
		// a PreToolUse hook that was never the delivery path. A plugin resource
		// is read whole; a guard that reads one field of it is a guard on one
		// field.
		published := []string{p.buys, p.verify, p.install}
		for _, st := range p.setup {
			published = append(published, st.Do, st.Check, st.IfNot)
		}
		all := strings.Join(published, "\n")

		for _, ev := range events {
			if wakes[ev] || !strings.Contains(all, ev) {
				continue
			}
			t.Errorf("%s advertises %s as a delivery moment somewhere in its published "+
				"text, and %s binds hook_poll only to %v. An agent reading that stops "+
				"polling and loses mail", p.harness, ev, hooksPath, keys(wakes))
		}
	}
	if checked == 0 {
		t.Fatal("no plugin hooks file was read, so this check verified nothing")
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// "on your next tool call" is the same promise without naming a hook.
//
// The event-name check above cannot see it: the sentence describes a delivery
// moment in prose. It is the exact wording that had an operator stop polling,
// so it is worth refusing by name until something actually delivers per-call.
func TestNoPluginPromisesPerToolCallDelivery(t *testing.T) {
	for _, p := range catalog {
		published := []string{p.buys, p.verify, p.install}
		for _, st := range p.setup {
			published = append(published, st.Do, st.Check, st.IfNot)
		}
		for _, text := range published {
			if strings.Contains(text, "next tool call") {
				t.Errorf("%s promises delivery on the next tool call. Nothing binds "+
					"hook_poll to a per-call event, so mail arrives at a turn boundary: %q",
					p.harness, text)
			}
		}
	}
}
