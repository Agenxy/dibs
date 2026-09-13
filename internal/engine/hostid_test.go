package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// One computer, one name: when the address plane (Supgang) knows this
// machine, its id is what loopback callers are stamped with and what a
// remote agent is told apart by; the ledger's node id stands in only when
// nothing better is known, exactly as before the field existed.
func TestTheHostIDIsTheAddressPlanesWhenKnown(t *testing.T) {
	e := &Engine{state: core.NewState("ledger-node", core.DefaultLimits())}
	if e.HostID() != "ledger-node" {
		t.Fatalf("HostID with nothing set = %q, want the ledger's node id", e.HostID())
	}
	e.SetHostID(" fed08b444ee029ef ")
	if e.HostID() != "fed08b444ee029ef" || e.NodeID() != "ledger-node" {
		t.Errorf("HostID = %q NodeID = %q: the address plane's id names the computer, the ledger keeps its own", e.HostID(), e.NodeID())
	}
	e.SetHostID("")
	if e.HostID() != "ledger-node" {
		t.Errorf("clearing the host id did not fall back to the node id: %q", e.HostID())
	}
}
