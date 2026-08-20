package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// The panel copy must carry no message bodies, in the shape production builds.
//
// SIXTH TIME THIS LEAK HAS BEEN FIXED, and the second time the fix was shaped
// to data the code does not produce. redactBodies handled *core.Message,
// core.Message, []*core.Message, core.Result, map[string]any and []any, and the
// production panel builds its inbox as []map[string]any. That matches none of
// them, so it reached the default branch and was returned with every body
// intact, while the tests passed because they called the redactor directly with
// a typed []*core.Message.
//
// This one goes through panelPayload, which is what the server actually calls,
// so it cannot pass by being handed a shape the server never makes.
func TestThePanelPayloadCarriesNoBodies(t *testing.T) {
	msgs := []*core.Message{
		{
			Serial: 1, From: "peer", To: "me", Type: core.MsgQuestion,
			Body: "SECRET-QUESTION-BODY", Response: "SECRET-RESPONSE-BODY",
		},
		{Serial: 2, From: "peer", To: "me", Type: core.MsgNotify, Body: "SECRET-NOTIFY-BODY"},
	}
	res := core.Result{
		"agent_id": "me",
		"inbox":    core.Result{"messages": msgs},
	}

	payload := panelPayload(res)
	redacted := redactBodies(payload)

	blob, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	for _, secretText := range []string{
		"SECRET-QUESTION-BODY", "SECRET-RESPONSE-BODY", "SECRET-NOTIFY-BODY",
	} {
		if strings.Contains(string(blob), secretText) {
			t.Errorf("the panel copy still carries %q. It goes to every viewer of the "+
				"panel, and cached older panel scripts push it back through "+
				"ui/update-model-context, which is the exact leak this redaction "+
				"exists to stop:\n  %.400s", secretText, blob)
		}
	}

	// And the redaction did not simply empty the payload: the panel is useless
	// if it cannot say a message exists.
	if !strings.Contains(string(blob), "peer") {
		t.Errorf("the panel copy lost the sender too, so this proves nothing about "+
			"redaction: %.200s", blob)
	}
}

// The WHOLE result, as a host receives it, carries no bodies outside `content`.
//
// The previous test serialised panelMeta, and there are two other carriers:
// detail:true puts the complete board into the bootstrap, and a host that
// declares UI support gets the panel payload merged there too. A panel cached
// from an older build still contains the code that pushes every body back
// through ui/update-model-context, so both handed it exactly what the redaction
// had removed. Testing the redactor rather than the result is what let that
// stand. Found by a pre-release review, on the fix for the same leak.
func TestOnlyTheAgentsOwnContentCarriesBodies(t *testing.T) {
	msgs := []*core.Message{
		{Serial: 1, From: "peer", To: "me", Type: core.MsgNotify, Body: "SECRET-IN-BOOTSTRAP"},
	}
	// act_token is what makes panelBootstrap return anything at all, and the
	// bootstrap is the carrier under test. Without it this exercised nothing and
	// passed against the leaking code, which is the trap this whole test exists
	// to stop falling into.
	res := core.Result{
		"agent_id": "me", "act_token": "tok", "view": "mail",
		"inbox": core.Result{"messages": msgs},
	}

	for _, tc := range []struct {
		name               string
		detail, declaredUI bool
	}{
		{"plain", false, false},
		{"detail requested by the model", true, false},
		{"a host that declares UI support", false, true},
		{"both", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := showBoardResult(res, tc.detail, tc.declaredUI)

			// `content` is the agent's own channel and may carry anything.
			content := out["content"]
			delete(out, "content")
			blob, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(blob), "SECRET-IN-BOOTSTRAP") {
				t.Errorf("a body reached a carrier the panel can read:\n  %.500s", blob)
			}
			if content == nil {
				t.Error("no content: the agent must still get its own result")
			}
		})
	}
}
