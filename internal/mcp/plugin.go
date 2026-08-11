package mcp

import (
	"encoding/json"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/plugins"
)

// pluginDoc is the payload of dibs://plugin: every plugin, with its files and
// its setup procedure.
//
// Every plugin rather than a guess at the caller's, because resources/read
// carries no identity: there is nothing in it to match a harness against, and
// inventing one would mean an agent whose harness we guessed wrong is told the
// wrong thing with the same confidence as one we guessed right. The list is
// small, the payload is a few kilobytes, and the agent knows perfectly well
// which harness it is running on. register, which DOES know the harness
// because the agent just said so, is where the specific recommendation belongs.
func pluginDoc() string {
	body := map[string]any{
		"note": "Dibs works with no plugin at all: every tool behaves the same " +
			"without one. What a plugin buys is DELIVERY: on some harnesses it turns " +
			"mail from something you must remember to poll for into something that " +
			"arrives in your session. Find your harness below, follow `setup` in " +
			"order, and check each step rather than assuming it took.",
		"harnesses":   plugins.Names(),
		"plugins":     plugins.All(),
		"marketplace": json.RawMessage(plugins.Marketplace()),
	}
	out, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		// Static, embedded input: unreachable, and a panic in a read handler
		// would take the daemon down for a documentation request.
		return `{"error":"plugin catalogue could not be rendered"}`
	}
	return string(out)
}

// pluginHint is the one-time nudge attached to a fresh registration.
//
// This is the moment it belongs, and it took a while to see why. An agent
// connects, is handed forty tools, and has no way to learn that its particular
// harness has a hook that would wake it: that lived in a README, in a
// repository the agent may never have cloned. So the first time an agent says
// what harness it is, Dibs answers with what that harness can do.
//
// Only on a FRESH registration. A reattach is the same agent coming back after
// losing context, and repeating an install prompt to somebody who has already
// decided is how a useful hint becomes noise that gets filtered out: including
// the one time it mattered.
//
// It never asserts the plugin is missing, because the daemon cannot see that.
// What it CAN see is whether a lifecycle hook fired for this session, which is
// a different and better fact: it is the difference between installed on disk
// and actually loaded, and it is the one an agent cannot check for itself.
// Reported as an observation ("no hook has reached this daemon") rather than a
// conclusion ("you have not installed it"), because a plugin installed earlier
// this session is real and still inert until the next one.
func pluginHint(harness string, reattached, hooksLive bool) map[string]any {
	if reattached || harness == "" {
		return nil
	}
	p, ok := plugins.For(harness)
	if !ok {
		return nil
	}
	// Answer the question rather than handing over homework.
	//
	// SessionStart fires before the agent gets a turn, so by the time it reaches
	// register the daemon already knows whether that hook arrived. Telling an
	// agent to go and verify something the server can already see would be busywork
	// on its first turn, and busywork is how a first-connection nudge gets learned
	// as noise.
	//
	// The negative is stated carefully. No hook traffic is not proof the plugin is
	// absent: the agent may have installed it this session, where hooks stay inert
	// until the next one, so the sentence names the observation, not a conclusion.
	hint := map[string]any{
		"harness":    p.Harness,
		"buys":       p.Buys,
		"hooks_live": hooksLive,
		"read":       "dibs://plugin",
	}
	if hooksLive {
		hint["status"] = "Your lifecycle hooks reached this daemon before you registered, " +
			"so the plugin is installed AND loaded. Nothing to do."
		// Hook traffic is not delivery, and conflating them told a Codex agent it
		// could stop polling while its own catalogue entry said mail is pull-only
		// on that harness: two contradictory sentences in one result, the first
		// of which loses mail if believed. Hooks firing means the harness talks to
		// this daemon; whether anything can WAKE the agent is a separate fact the
		// catalogue holds.
		if p.Delivers {
			hint["note"] = "shown once, on first registration. Mail will arrive in your " +
				"session; you do not need to poll inbox to find it"
		} else {
			hint["note"] = "shown once, on first registration. Your hooks reach this " +
				"daemon, but this harness has no wake path: mail is still PULL-ONLY " +
				"here, so keep calling check_in each activation and await_events when " +
				"you are about to block"
		}
		return hint
	}
	hint["status"] = "No lifecycle hook has reached this daemon for your session. That " +
		"is not proof the plugin is missing: hooks are read at session start, so one " +
		"installed during this session stays inert until the next, but it does mean " +
		"nothing is waking you right now."
	hint["note"] = "shown once, not repeated on reattach: read dibs://plugin for the " +
		"files and an ordered setup procedure, each step with its own check"
	hint["verify"] = p.Verify
	return hint
}

// attachPluginHint adds the hint to a registration result when there is one to
// give. Separated so the registration path stays about registration.
func attachPluginHint(res core.Result, harness string, reattached, hooksLive bool) core.Result {
	if res == nil {
		return res
	}
	if hint := pluginHint(harness, reattached, hooksLive); hint != nil {
		res["plugin"] = hint
	}
	return res
}
