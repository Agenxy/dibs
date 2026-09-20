package mcp

import "testing"

// A harness whose bridge names the session `host-<ppid>` and whose hooks
// name it by UUID is still one session.
//
// Gemini CLI's SessionStart hook posts its own session UUID through `dibs
// hook-poll`; its MCP bridge registers the agent under the synthetic
// `host-<ppid>` and sends that same id as `_meta com.dibs/session` on every
// call. The engine infers a session by directory only when no alias
// arrives, and the bridge's synthetic id arrived as one: it said nothing
// the register did not already say, and it switched the inference off, so
// the UUID stayed unbound and every later hook for that session resolved
// to nobody. The hook reported success and delivered nothing; the plugin
// README's "register, and the next session start finds it" was not true.
// Reproduced by the pre-release review, round nineteen. An alias that is
// not a thread and that the row already holds leaves the inference on.
func TestAGeminiSessionIsBoundDespiteTheBridgesSyntheticAlias(t *testing.T) {
	const uuid = "b7804476-3292-4ad9-bddb-16f823328751"
	bridge := map[string]any{SessionMetaKey: "host-777"}
	for _, order := range []string{"hook then register", "register then hook then check_in"} {
		t.Run(order, func(t *testing.T) {
			srv, _, _ := newServerWithEngine(t)
			poll := func() map[string]any {
				return toolCall(t, srv, "hook_poll", map[string]any{
					"session_id": uuid, "event": "SessionStart", "cwd": "/w/repo",
				})
			}
			if order == "hook then register" {
				poll() // SessionStart fires before the model has called anything
			}
			reg := toolCallWithMeta(t, srv, "register", map[string]any{
				"name": "gem", "cwd": "/w/repo", "session_id": "host-777",
			}, bridge)
			if reg["token"] == nil {
				t.Fatalf("setup: %v", reg)
			}
			toolCallWithMeta(t, srv, "check_in", map[string]any{"token": reg["token"]}, bridge)
			if order != "hook then register" {
				poll() // a later session start, after the agent is on the board
				toolCallWithMeta(t, srv, "check_in", map[string]any{"token": reg["token"]}, bridge)
			}
			if res := poll(); res["agent"] != "gem" {
				t.Fatalf("%s: the hook for the session's own UUID resolves to %v, want gem: the "+
					"bridge's synthetic alias switched the directory inference off", order, res["agent"])
			}
		})
	}
}
