package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// Saying "nowhere" is different from saying nothing, and the daemon has to
// hear the difference.
//
// resolveHostID stamps a loopback caller with this daemon's own identity, on
// the reasoning that nothing off this machine reaches loopback. That was true
// of every caller Dibs had. A tunnel relaying a ChatGPT conversation into
// `dibs mcp-stdio` is the thing that makes it false: the calls arrive on
// loopback and are not from this machine, or from any machine.
func TestACallerThatSaysItIsNowhereIsNotStampedWithThisMachine(t *testing.T) {
	ctx := withTransportHost(t.Context(), true, "this-daemons-node-id")

	silent := json.RawMessage(`{"_meta":{}}`)
	if got := resolveHostID(ctx, silent); got != "this-daemons-node-id" {
		t.Fatalf("a loopback caller that asserts nothing is still this machine's; got %q", got)
	}

	hostless := json.RawMessage(`{"_meta":{"com.dibs/hostless":true}}`)
	if got := resolveHostID(ctx, hostless); got != "" {
		t.Errorf("a caller that states it is on no computer was stamped as %q. Every rule "+
			"that reads a host then treats a browser tab as working on this filesystem", got)
	}

	// A stated host still wins over both, which is how a joined machine's
	// bridge names itself.
	stated := json.RawMessage(`{"_meta":{"com.dibs/host":"another-machine"}}`)
	if got := resolveHostID(ctx, stated); got != "another-machine" {
		t.Errorf("a bridge's own host id must still win; got %q", got)
	}
}

// A claim says "I am working in this directory" and can block somebody who
// is. Both halves need the claimant to be on that filesystem.
//
// Measured before this refusal existed: a relayed conversation took an
// EXCLUSIVE claim on a checkout on somebody else's Mac, and the board reported
// no overlap, because it had been stamped with the tunnel's host and there was
// nothing there to overlap with.
func TestAParticipantWithNoMachineCannotClaimAPath(t *testing.T) {
	hostless := json.RawMessage(`{"_meta":{"com.dibs/hostless":true}}`)
	err := refuseHostlessClaim(hostless, "/Users/someone/Desktop/Dibs")
	if err == nil {
		t.Fatal("a participant with no computer must not claim a path on somebody else's")
	}
	// Rule 6: every error carries a hint naming the corrective call, and the
	// hint is a FIELD rather than part of the message, which is how the agent
	// receives it. `declare` is the point of the refusal being narrow: it says
	// what you are working on, names no path and blocks nobody.
	var ce *core.Error
	if !errors.As(err, &ce) {
		t.Fatalf("the refusal has to be a core.Error so the agent gets code and hint; got %T", err)
	}
	if ce.Code != "E_NO_MACHINE" {
		t.Errorf("code = %q, want E_NO_MACHINE", ce.Code)
	}
	if !strings.Contains(ce.Hint, "declare") {
		t.Errorf("the hint has to name what to do instead; got %q", ce.Hint)
	}
	if !strings.Contains(ce.Msg, "/Users/someone/Desktop/Dibs") {
		t.Errorf("the refusal has to name the path refused; got %q", ce.Msg)
	}

	// And an ordinary caller is untouched. This refusal is about one new kind
	// of participant, not a new rule for the fleet.
	ordinaryCallers := []string{
		`{"_meta":{}}`,
		`{}`,
		`{"_meta":{"com.dibs/host":"some-machine"}}`,
	}
	for _, ordinary := range ordinaryCallers {
		if err := refuseHostlessClaim(json.RawMessage(ordinary), "/w/repo"); err != nil {
			t.Errorf("an ordinary caller (%s) was refused a claim: %v", ordinary, err)
		}
	}
}
