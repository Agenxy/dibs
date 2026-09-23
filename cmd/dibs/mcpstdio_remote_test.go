package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A relayed conversation must arrive on the board carrying nothing about the
// machine that relayed it.
//
// The bridge's whole job, for every harness Dibs has had, is to observe this
// computer so the model does not have to be asked: host, working directory,
// checkout, branch, pid, session. Those are right because the harness and the
// bridge are the same process tree. OpenAI's Secure MCP Tunnel breaks that: it
// runs `dibs mcp-stdio` on somebody's Mac and pipes in a ChatGPT conversation
// that has no directory, no repository, no process and no machine.
//
// Measured before the flag existed, against the real board: such a register
// recorded `cwd: /private/tmp, host: MacMarine`, and the conversation then took
// an exclusive claim on a checkout it cannot see. A path is evidence on ONE
// computer, and every rule that reads one was being handed a borrowed answer.
func TestARelayedConversationStampsNothingAboutThisMachine(t *testing.T) {
	restore := remoteSession
	remoteSession = true
	t.Cleanup(func() { remoteSession = restore; remoteRegisterSession = "" })

	in := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{` +
		`"_meta":{"openai/session":"conv_abc123"},"name":"register",` +
		`"arguments":{"name":"cg","description":"d","kind":"ephemeral","nonce":"n1"}}}`
	var msg map[string]any
	if err := json.Unmarshal(enrichRegister([]byte(in)), &msg); err != nil {
		t.Fatal(err)
	}
	params, _ := msg["params"].(map[string]any)
	args, _ := params["arguments"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)

	// Each of these is an observation about the machine running the tunnel,
	// and each was being asserted about a browser tab.
	for _, k := range []string{"cwd", "host", "branch", "pid"} {
		if v, present := args[k]; present {
			t.Errorf("register carries %s=%v, which is the tunnel's and not the caller's", k, v)
		}
	}
	for _, k := range []string{"com.dibs/host", "com.dibs/repo"} {
		if v, present := meta[k]; present {
			t.Errorf("_meta carries %s=%v, which describes this computer", k, v)
		}
	}
	// And it says so, rather than leaving the daemon to read silence from
	// loopback as "here", which is what it does for every other caller.
	if hostless, _ := meta["com.dibs/hostless"].(bool); !hostless {
		t.Error("the bridge has to STATE that the caller is on no computer: a blank host " +
			"from loopback is stamped with the daemon's own")
	}
	// The conversation's own id, not this process's. `host-<ppid>` here would
	// key every ChatGPT conversation coming through one tunnel to the same
	// session, and they would reattach to each other's agent.
	if got, _ := args["session_id"].(string); got != "conv_abc123" {
		t.Errorf("session_id = %q, want the conversation id the client sent", got)
	}
	if got, _ := meta["com.dibs/session"].(string); got != "conv_abc123" {
		t.Errorf("_meta session = %q, want the conversation id", got)
	}
}

// Absence is tolerated rather than filled in. The field's own documentation
// says servers "must tolerate their absence", and a made-up session id binds
// the agent to a conversation that does not exist.
func TestAConversationThatNamesNoSessionGetsNoneInvented(t *testing.T) {
	restore := remoteSession
	remoteSession = true
	t.Cleanup(func() { remoteSession = restore; remoteRegisterSession = "" })

	in := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"register",` +
		`"arguments":{"name":"cg","description":"d","kind":"ephemeral","nonce":"n1"}}}`
	var msg map[string]any
	if err := json.Unmarshal(enrichRegister([]byte(in)), &msg); err != nil {
		t.Fatal(err)
	}
	params, _ := msg["params"].(map[string]any)
	args, _ := params["arguments"].(map[string]any)
	if v, present := args["session_id"]; present && v != "" {
		t.Errorf("session_id = %v: nothing named one, and host-<ppid> would key every "+
			"conversation through this tunnel to the same agent", v)
	}
}

// And the ordinary path is untouched: a harness on this machine still gets
// everything observed for it, which is the reason the bridge exists.
func TestALocalHarnessStillGetsThisMachineObserved(t *testing.T) {
	restore := remoteSession
	remoteSession = false
	t.Cleanup(func() { remoteSession = restore })

	in := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"register",` +
		`"arguments":{"name":"local","description":"d","kind":"ephemeral","nonce":"n1"}}}`
	var msg map[string]any
	if err := json.Unmarshal(enrichRegister([]byte(in)), &msg); err != nil {
		t.Fatal(err)
	}
	params, _ := msg["params"].(map[string]any)
	args, _ := params["arguments"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	if cwd, _ := args["cwd"].(string); cwd == "" {
		t.Error("a local harness must still have its working directory observed")
	}
	if pid, _ := args["pid"].(float64); pid == 0 {
		t.Error("a local harness must still have the bridge's pid, which is its liveness")
	}
	if hostless, _ := meta["com.dibs/hostless"].(bool); hostless {
		t.Error("a local harness is on a computer and must not be marked otherwise")
	}
}

// A flag this bridge does not know is refused, because a bridge is configured
// once in a file by somebody who will never see a warning, and a typo that is
// ignored puts a false host on somebody else's board.
func TestAMisspeltBridgeFlagIsRefused(t *testing.T) {
	err := parseBridgeArgs([]string{"--remot-session"})
	if err == nil {
		t.Fatal("an unknown argument must be refused, not ignored")
	}
	if !strings.Contains(err.Error(), remoteSessionFlag) {
		t.Errorf("the refusal should show the flag that exists; got %q", err)
	}
	remoteSession = false
	if err := parseBridgeArgs([]string{remoteSessionFlag}); err != nil {
		t.Fatalf("the flag itself must be accepted: %v", err)
	}
	if !remoteSession {
		t.Error("the flag did not take effect")
	}
	remoteSession = false
}
