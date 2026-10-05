package mcp

import "testing"

// Enter through tools/call over real HTTP and the ledger-backed engine, not
// Apply: the regression lives in ingress admission. Done is a final report,
// but neither a progress note nor completion invents intermediate reports.
func TestDoneFinalMilestoneAcceptanceThroughMCP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			srv, _, _ := newServerWithEngine(t)
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				r := toolCallOn(t, srv, version, name, args)
				if r["code"] != nil {
					t.Fatalf("setup %s failed: %v", name, r)
				}
				return r
			}
			if version == "2025-11-25" {
				r := rpc(t, srv, "", "initialize", map[string]any{
					"protocolVersion": version,
					"capabilities":    map[string]any{},
					"clientInfo":      map[string]any{"name": "milestone-legacy", "version": "1"},
				})
				if r["result"].(map[string]any)["protocolVersion"] != version {
					t.Fatalf("setup legacy handshake failed: %v", r)
				}
			}
			tokens := map[string]string{}
			for _, id := range []string{"lead", "worker", "stranger"} {
				r := call("register", map[string]any{"name": id, "nonce": "done-milestone-" + id})
				tok, ok := r["token"].(string)
				if !ok || tok == "" {
					t.Fatalf("setup register omitted token: %v", r)
				}
				tokens[id] = tok
				call("check_in", map[string]any{"token": tok})
			}
			parent := call("send", map[string]any{
				"token": tokens["lead"], "to": "worker", "type": "request",
				"body": "prepare and deliver", "milestones": []string{"preparation", "final delivery"},
			})["msg_serial"]
			if parent == nil {
				t.Fatal("setup send omitted request serial")
			}
			call("respond", map[string]any{"token": tokens["worker"], "msg_serial": parent, "disposition": "approve"})
			call("respond", map[string]any{"token": tokens["worker"], "msg_serial": parent, "disposition": "progress", "body": "preparing final artifact"})
			accept := map[string]any{"token": tokens["lead"], "msg_serial": parent, "disposition": "accept", "milestone": 2}
			unfinished := toolCallOn(t, srv, version, "respond", accept)
			if unfinished["code"] != "E_MILESTONE_UNREPORTED" {
				t.Fatalf("index-free progress made unfinished final milestone acceptable: %v", unfinished)
			}
			const artifact = "artifact:final-delivery"
			call("respond", map[string]any{"token": tokens["worker"], "msg_serial": parent, "disposition": "done", "body": "final artifact delivered", "deliverable": artifact})
			before := call("read_mail", map[string]any{"token": tokens["lead"], "msg_serial": parent})
			message := before["message"].(map[string]any)
			if message["state"] != "done" || message["deliverable"] != artifact {
				t.Fatalf("setup did not complete exact request: %v", message)
			}
			initial := before["milestone_reviews"].([]any)[1].(map[string]any)
			if initial["status"] != "unreviewed" || initial["at"] != nil {
				t.Fatalf("done invented a milestone review: %v", initial)
			}
			accept["milestone"] = 1
			earlier := toolCallOn(t, srv, version, "respond", accept)
			if earlier["code"] != "E_MILESTONE_UNREPORTED" {
				t.Fatalf("done invented an intermediate report: %v", earlier)
			}
			accept["milestone"], accept["token"] = 2, tokens["stranger"]
			stranger := toolCallOn(t, srv, version, "respond", accept)
			if stranger["code"] != "E_NO_MESSAGE" {
				t.Fatalf("done allowed a stranger to review: %v", stranger)
			}
			accept["token"] = tokens["lead"]
			accepted := toolCallOn(t, srv, version, "respond", accept)
			if accepted["ok"] != true || accepted["review"] != "accepted" {
				t.Fatalf("DONE final milestone falsely refused through MCP: %v", accepted)
			}
			after := call("read_mail", map[string]any{"token": tokens["lead"], "msg_serial": parent})
			reviews := after["milestone_reviews"].([]any)
			final := reviews[1].(map[string]any)
			if final["status"] != "accepted" || final["by"] != "lead" || final["at"] == nil || reviews[0].(map[string]any)["status"] != "unreviewed" {
				t.Fatalf("final review missing or intermediate review invented: %v", reviews)
			}
			completed := after["message"].(map[string]any)
			if completed["state"] != "done" || completed["deliverable"] != artifact || completed["response"] != message["response"] {
				t.Fatalf("review changed original done verdict or artifact: %v", completed)
			}
		})
	}
}
