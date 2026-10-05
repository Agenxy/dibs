package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Enter through authenticated relay HTTP, then read the sender's actual mail.
// Neither hand-setting a presentation flag nor accepting the helper's proposed
// interruption level establishes that the notification was visible.
func TestHumanSettingsThroughAuthenticatedRelayReceipt(t *testing.T) {
	h := newRelayHarness(t)
	h.enroll()
	headers := map[string]string{"Authorization": "Bearer " + h.session()}
	ctx := context.Background()
	human, _, err := h.eng.HumanAgent(ctx)
	if err != nil || human == "" {
		t.Fatalf("human setup: %q %v", human, err)
	}
	r, err := h.eng.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "settings-requester", Nonce: "settings-receipt-fixture"})
	if err != nil || r["token"] == nil {
		t.Fatalf("sender setup: %v %v", r, err)
	}
	token := r["token"].(string)
	for _, tc := range []struct {
		name, style, sensitive, hint string
		unknown                      bool
	}{
		{"banner", "banner", "enabled", "System Settings > Notifications > Dibs > Alerts", false},
		{"silent", "none", "enabled", "System Settings > Notifications > Dibs > Alerts", false},
		{"alert", "alert", "enabled", "Focus is not observable", false},
		{"disabled-sensitive", "alert", "disabled", "Time Sensitive", false},
		{"unprovisioned", "alert", "not-supported", "this build is not provisioned", false},
		{"old-receipt", "old", "enabled", "settings are unknown", true},
		{"malformed-receipt", "malformed", "enabled", "settings are unknown", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent, err := h.eng.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: token, To: human, MsgType: core.MsgRequest, Body: "settings fixture approval"})
			serial, ok := sent["msg_serial"].(uint64)
			if err != nil || !ok || serial == 0 {
				t.Fatalf("send setup: %v %v", sent, err)
			}
			settings := map[string]any{
				"version": 1, "authorization_status": "authorized", "alert_style": tc.style,
				"alert_setting": "enabled", "notification_center_setting": "enabled",
				"lock_screen_setting": "enabled", "time_sensitive_setting": tc.sensitive,
				"focus": map[string]any{"authorization": "not-determined", "observable": false},
			}
			body := map[string]any{"serial": serial, "state": "posted", "interruption_level": "timeSensitive"}
			if tc.style != "old" {
				body["settings"] = settings
			}
			if tc.style == "malformed" {
				body["settings"] = map[string]any{"version": 1, "alert_style": 42}
			}
			if code, _ := h.post("/api/human/delivery", nil, body); code != http.StatusUnauthorized {
				t.Fatalf("metadata bypassed relay authentication: HTTP%d", code)
			}
			if code, out := h.post("/api/human/delivery", headers, body); code != http.StatusOK {
				t.Fatalf("authenticated settings receipt: HTTP%d %v", code, out)
			}
			read, err := h.eng.GetMessage(ctx, token, serial)
			if err != nil || read["human_delivery"] == nil {
				t.Fatalf("read_mail setup: %v %v", read, err)
			}
			raw, err := json.Marshal(read["human_delivery"])
			if err != nil {
				t.Fatal(err)
			}
			var delivery struct {
				Posted   bool `json:"posted"`
				Receipts map[string]struct {
					Posted   bool            `json:"posted"`
					Shown    string          `json:"shown"`
					Settings json.RawMessage `json:"settings"`
					Hint     string          `json:"hint"`
				} `json:"receipts"`
			}
			if err := json.Unmarshal(raw, &delivery); err != nil {
				t.Fatal(err)
			}
			receipt, ok := delivery.Receipts["relay-1"]
			if !ok || !delivery.Posted || !receipt.Posted || receipt.Shown != "unconfirmed" {
				t.Fatalf("OS acceptance was not retained separately from visibility: %s", raw)
			}
			if len(receipt.Settings) == 0 || (string(receipt.Settings) == "null") != tc.unknown {
				t.Fatalf("settings evidence/unknown missing: %s", raw)
			}
			if !strings.Contains(receipt.Hint, tc.hint) {
				t.Fatalf("receipt lacks %q: %s", tc.hint, raw)
			}
			if tc.unknown {
				return
			}
			var measured map[string]any
			if err := json.Unmarshal(receipt.Settings, &measured); err != nil || measured["alert_style"] != tc.style || measured["time_sensitive_setting"] != tc.sensitive {
				t.Fatalf("helper settings were replaced by an inference: %s %v", raw, err)
			}
			// Later state-only receipts do not erase the source's actual settings.
			if code, out := h.post("/api/human/delivery", headers, map[string]any{"serial": serial, "state": "dismissed"}); code != http.StatusOK {
				t.Fatalf("dismissal: HTTP%d %v", code, out)
			}
			after, err := h.eng.GetMessage(ctx, token, serial)
			if err != nil {
				t.Fatal(err)
			}
			bytes, err := json.Marshal(after["human_delivery"])
			if err != nil || !strings.Contains(string(bytes), `"alert_style":"`+tc.style+`"`) {
				t.Fatalf("dismissal erased settings evidence: %s %v", bytes, err)
			}
		})
	}
}
