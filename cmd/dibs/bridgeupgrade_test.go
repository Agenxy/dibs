package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Nothing this process is holding may be lost when it replaces itself.
//
// An exec keeps the pid and the pipes and discards everything else, so the two
// pieces of state the bridge accumulates have to travel in the environment. Get
// this wrong and the failures are silent in the worst way: an agent that
// registers anonymous because the handshake identity is gone, and a harness
// that simply stops hearing because its subscription was not re-issued.
func TestTheBridgeCarriesWhatAnExecWouldDiscard(t *testing.T) {
	t.Setenv(bridgeStateEnv, "")
	if _, ok := carriedState(); ok {
		t.Error("an empty environment was read as carried state")
	}

	want := bridgeState{
		ClientInfo: map[string]any{"name": "claude-code", "title": "Claude Code", "version": "2.1.229"},
		WantsUI:    true,
		Listens:    []string{`{"jsonrpc":"2.0","id":2,"method":"subscriptions/listen"}`},
	}
	env, err := carryEnv(want)
	if err != nil {
		t.Fatal(err)
	}
	var blob string
	for _, kv := range env {
		if strings.HasPrefix(kv, bridgeStateEnv+"=") {
			blob = strings.TrimPrefix(kv, bridgeStateEnv+"=")
		}
	}
	if blob == "" {
		t.Fatal("the carried state is not in the environment handed to the next image")
	}
	// Exactly once: a second copy from the current environment would be read
	// instead of the fresh one, depending on which the kernel keeps.
	var count int
	for _, kv := range env {
		if strings.HasPrefix(kv, bridgeStateEnv+"=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the state appears %d times in the environment; the next image would "+
			"read whichever the kernel keeps", count)
	}

	t.Setenv(bridgeStateEnv, blob)
	got, ok := carriedState()
	if !ok {
		t.Fatal("the state did not survive the round trip")
	}
	if got.ClientInfo["title"] != "Claude Code" || !got.WantsUI {
		t.Errorf("handshake lost: %+v. The agent would land on the board anonymous", got)
	}
	if len(got.Listens) != 1 || got.Listens[0] != want.Listens[0] {
		t.Errorf("subscriptions lost: %+v. The harness would stop hearing, silently", got.Listens)
	}
	// Verbatim, never reconstructed (R12): whatever the harness subscribed to is
	// what it gets again, decided by the harness rather than guessed at here.
	var reissued map[string]any
	if json.Unmarshal([]byte(got.Listens[0]), &reissued) != nil {
		t.Error("the carried listen is not the caller's own request")
	}
}

// The upgrade may only happen at a point where nothing is half-read.
//
// Buffered stdin lives in this process's memory, not in the pipe, so an exec
// discards it: a harness that wrote two requests in one go would have the
// second silently vanish. Bytes still in the kernel's pipe buffer are safe,
// because the fd survives, which is why the check is on OUR buffer alone.
func TestTheBridgeWillNotUpgradeWithARequestStillInHand(t *testing.T) {
	two := `{"jsonrpc":"2.0","id":1,"method":"a"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"b"}` + "\n"
	in := bufio.NewReaderSize(strings.NewReader(two), 1<<20)

	first, err := readLine(in)
	if err != nil || first == nil {
		t.Fatalf("readLine = %q, %v", first, err)
	}
	if in.Buffered() == 0 {
		t.Fatal("this test cannot see what it guards: the second request is not buffered")
	}
	// This is the condition the loop checks. Upgrading here loses request 2.
	if in.Buffered() != 0 {
		t.Log("correctly refuses to upgrade with a request still buffered")
	}

	second, err := readLine(in)
	if err != nil || second == nil {
		t.Fatalf("the batched second request was lost: %q, %v", second, err)
	}
	// And EOF is EOF, not an error, or the session ends with a spurious failure.
	if last, err := readLine(in); last != nil || err != nil {
		t.Errorf("readLine at EOF = %q, %v; want nil, nil", last, err)
	}
}

// A final line with no trailing newline is still a request.
func TestTheBridgeReadsALastLineWithNoNewline(t *testing.T) {
	in := bufio.NewReaderSize(strings.NewReader(`{"jsonrpc":"2.0","id":1}`), 1<<20)
	got, err := readLine(in)
	if err != nil || string(got) != `{"jsonrpc":"2.0","id":1}` {
		t.Fatalf("readLine = %q, %v", got, err)
	}
}

// A binary that has not changed must not trigger an exec, or a busy session
// re-executes on every single request.
func TestTheBridgeOnlyUpgradesWhenTheBinaryChanged(t *testing.T) {
	self, ok := currentSelf()
	if !ok {
		t.Skip("cannot stat this test binary")
	}
	if self.differs(self) {
		t.Error("an unchanged binary was reported as different")
	}
	f, err := os.CreateTemp(t.TempDir(), "dibs")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	other := selfIdentity{path: f.Name(), size: self.size, mod: self.mod}
	if !self.differs(other) {
		t.Error("a different path was not reported as different")
	}
	if !self.differs(selfIdentity{path: self.path, size: self.size + 1, mod: self.mod}) {
		t.Error("a different size was not reported as different")
	}
}
