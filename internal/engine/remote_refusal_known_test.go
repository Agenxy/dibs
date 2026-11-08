// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import "testing"

// A host whose bridge cannot take a no-navigation (archived) plan refuses
// before any handoff, and that refusal is KNOWN: recordNonRetryableWake skips
// a routeRefused plan, and anything else is reported to senders as an unknown
// native reply. This branch used to mark NoRetry on a copy and never set
// routeRefused, so an archived wake that reached it rather than the route
// refresh read as unknown. TestRemoteRouteChangeThroughProductionWake caught
// it only under load, because which branch an archived wake reaches is timing.
func TestAHostThatCannotTakeAnArchivedPlanRefusesKnowingly(t *testing.T) {
	e := &Engine{}
	e.hostWakes.bridges = map[string]*hostBridge{
		"other-host": {harnesses: map[string]bool{"codex": true}},
	}
	plan := wakePlan{host: "other-host"}
	plan.request.Harness = "codex"
	plan.request.NoNavigation = true
	if _, id := e.enqueueRemoteWake(&plan, "worker"); id != 0 {
		t.Fatal("setup: a no-navigation plan was handed to a bridge that cannot take one")
	}
	if !plan.routeRefused || plan.nativeOutcome == nil || !plan.nativeOutcome.NoRetry {
		t.Fatalf("host refusal is not a known refusal: routeRefused=%v outcome=%+v", plan.routeRefused, plan.nativeOutcome)
	}
}
