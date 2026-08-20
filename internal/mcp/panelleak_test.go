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
