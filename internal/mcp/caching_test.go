package mcp

import "testing"

// Every cacheable result must carry a hint, and per-caller data must never be
// marked shareable.
//
// The 2026-07-28 spec requires ttlMs and cacheScope on complete results from
// server/discover, tools/list, resources/list and resources/read. The reason to
// test it rather than trust it is asymmetric: a missing or wrong ttlMs costs a
// refetch, but cacheScope is a PERMISSION. "public" tells shared gateways and
// caching proxies they may serve the response to a different caller —
// explicitly including one holding a different token. So marking one lane's
// mailbox public is not a performance mistake, it is disclosure.
//
// Driven through the real dispatcher rather than by inspecting the constants,
// because the constant being right is not the claim; the claim is that the
// handler applies the right one.
func TestCacheHintsArePresentAndCorrectlyScoped(t *testing.T) {
	srv, _ := newServer(t)

	for _, tc := range []struct {
		method    string
		wantScope string
		why       string
	}{
		{
			method: "server/discover", wantScope: scopePublic,
			why: "capabilities and instructions are identical for every caller",
		},
		{
			method: "tools/list", wantScope: scopePublic,
			why: "the tool list is the same for everyone and fixed per binary",
		},
		{
			method: "resources/list", wantScope: scopePublic,
			why: "the descriptors are static; the data behind them is hinted per read",
		},
	} {
		out := result(t, rpc(t, srv, "2026-07-28", tc.method, map[string]any{}), tc.method)
		ttl, ok := out["ttlMs"]
		if !ok {
			t.Errorf("%s carries no ttlMs; the spec requires a hint on every complete result "+
				"and a client that gets none must treat it as immediately stale — which for a "+
				"43-tool list means resending it on every cold path", tc.method)
			continue
		}
		if n, isNum := toInt(ttl); !isNum || n < 0 {
			t.Errorf("%s ttlMs = %v; servers MUST provide a value >= 0", tc.method, ttl)
		}
		if got := out["cacheScope"]; got != tc.wantScope {
			t.Errorf("%s cacheScope = %v, want %q — %s", tc.method, got, tc.wantScope, tc.why)
		}
	}
}

// The one that would be a security bug.
//
// lanes://inbox is a single lane's mail, authorised by that lane's token.
// lanes://board is what every agent is entitled to see. They must not carry the
// same sharing permission, and the difference is not cosmetic: a shared cache
// obeying "public" on the inbox would hand one agent's private mail to another.
func TestAPrivateMailboxIsNeverMarkedShareable(t *testing.T) {
	srv, _ := newServer(t)
	reg := toolCall(t, srv, "register_lane", map[string]any{"name": "cache-probe"})
	tok, _ := reg["token"].(string)
	if tok == "" {
		t.Fatalf("could not register a lane to read a mailbox with: %v", reg)
	}

	box := result(t, rpc(t, srv, "2026-07-28", "resources/read", map[string]any{
		"uri":   "lanes://inbox",
		"_meta": map[string]any{metaTokenKey: tok},
	}), "resources/read lanes://inbox")
	if got := box["cacheScope"]; got != scopePrivate {
		t.Fatalf("lanes://inbox cacheScope = %v, want %q.\n"+
			"  This resource is one lane's mail, keyed by its token. \"public\" tells shared\n"+
			"  gateways they may serve it to a caller with a different authorization context,\n"+
			"  so this is a disclosure bug and not a caching one.", got, scopePrivate)
	}

	board := result(t, rpc(t, srv, "2026-07-28", "resources/read",
		map[string]any{"uri": "lanes://board"}), "resources/read lanes://board")
	if got := board["cacheScope"]; got != scopePublic {
		t.Errorf("lanes://board cacheScope = %v, want %q — the board is what every agent is "+
			"entitled to see, and marking it private forfeits sharing for no gain", got, scopePublic)
	}

	// Live data must not claim the static TTL, or a client sits on a stale board
	// for an hour while the fleet moves underneath it.
	if n, _ := toInt(board["ttlMs"]); n >= ttlStatic {
		t.Errorf("lanes://board ttlMs = %v, which is the static hint; the board changes on "+
			"every event", board["ttlMs"])
	}
}

// result unwraps the JSON-RPC envelope, failing loudly rather than returning an
// empty map — a nil result would make every assertion below vacuously "missing"
// and blame the cache hints for a transport error.
func result(t *testing.T, envelope map[string]any, what string) map[string]any {
	t.Helper()
	if e, ok := envelope["error"]; ok {
		t.Fatalf("%s returned an error: %v", what, e)
	}
	r, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s returned no result object: %v", what, envelope)
	}
	return r
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
