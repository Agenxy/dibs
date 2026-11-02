// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package mcp

import "testing"

// The live survivor's ordinary activity changes no source custody. Exercise
// the survivor-initiated direction through the real HTTP consent pipeline.
func TestSelfRecoveryConsentSurvivesSurvivorTraffic(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		for _, activity := range []string{"check-in", "incoming-mail", "ack-mail"} {
			t.Run(version+"/"+activity, func(t *testing.T) {
				f := recoveryFixture(t, version)
				var notify any
				if activity == "ack-mail" {
					notify = f.call("send", map[string]any{"token": f.lead["token"], "to": f.into["agent_id"], "type": "notify", "body": "ordinary survivor FYI"})["msg_serial"]
				}
				request := recoveryRequest(f)
				switch activity {
				case "check-in":
					f.call("check_in", map[string]any{"token": f.into["token"]})
				case "incoming-mail":
					f.call("send", map[string]any{"token": f.lead["token"], "to": f.into["agent_id"], "type": "question", "body": "unmoved survivor mail"})
				case "ack-mail":
					f.call("ack", map[string]any{"token": f.into["token"], "msg_serial": notify})
				}
				approved := f.call("respond", map[string]any{"token": f.lead["token"], "msg_serial": request, "disposition": "approve"})
				if approved["recovered"] == nil {
					t.Fatalf("unmoved survivor traffic prevented self recovery: %v", approved)
				}
				f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail})
				f.stop()
				f.srv, f.e, f.stop = supportedMergeServer(t, f.dir)
				f.call("read_mail", map[string]any{"token": f.into["token"], "msg_serial": f.mail})
			})
		}
	}
}
