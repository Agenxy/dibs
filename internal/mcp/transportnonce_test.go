package mcp

import (
	"encoding/json"
	"testing"
)

// A configured transport identity must not break every other tool.
//
// THE REGRESSION THIS CATCHES, and it would have shipped. The transport nonce
// was counted as a supplied argument for EVERY tool, while the unknown-argument
// check exempts only `token`. So configuring a harness identity, which is the
// entire point of that feature, made `check_in` fail with
//
//	check_in does not take "nonce": check the tool's schema
//
// about an argument the agent never sent. check_in is the call every agent must
// make at the start of every activation, so the effect was that pinning an
// identity broke the agent that pinned it.
//
// Nothing here caught it because every existing test supplies the nonce as an
// ARGUMENT, which is what a model does. A harness puts it in the transport, and
// that path had no test at all. Found by a pre-release review running a live
// daemon rather than the suite.
func TestATransportNonceDoesNotBreakOrdinaryTools(t *testing.T) {
	srv, cancel := newServer(t)
	defer cancel()

	reg := callWithIdentity(t, srv, "pinned-nonce",
		`{"name":"pinned","description":"has a harness identity","kind":"persistent"}`)
	tok, _ := reg["token"].(string)
	if tok == "" {
		t.Fatalf("setup: %v", reg)
	}

	// Every one of these is an ordinary call that declares no `nonce`, made
	// while the harness is still presenting one.
	for _, tool := range []string{"check_in", "inbox", "board"} {
		t.Run(tool, func(t *testing.T) {
			out := callWithIdentityRaw(t, srv, "pinned-nonce", tool, `{"token":"`+tok+`"}`)
			body, _ := json.Marshal(out)
			if e, ok := out["error"].(map[string]any); ok {
				t.Fatalf("%s failed while a transport nonce was configured: %v\n"+
					"  An agent that pins its identity cannot then check in, which is "+
					"the one call it must make every activation", tool, e["message"])
			}
			if len(body) == 0 {
				t.Fatalf("%s returned nothing", tool)
			}
		})
	}
}
