package core

import (
	"strings"
	"testing"
)

// Ingress refuses an acceptance of an unreported index, but replay must still
// accept the historical op. Reporting step 3 is not reporting step 1.
func TestAcceptUnreportedMilestoneIsAdmitOnly(t *testing.T) {
	s, serial := taskRequest(t, "one", "two", "three")
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "progress", Milestone: 3, Body: "third proof"}, t0)
	op := &Op{Kind: OpRespond, Token: "tl", MsgSerial: serial, Disposition: "accept", Milestone: 1}
	before := s.Serial
	err := s.Admit(op)
	if codeOf(err) != "E_MILESTONE_UNREPORTED" || !strings.Contains(err.Error(), "3") {
		t.Errorf("unreported step accepted or missing corrective report indices: %v", err)
	}
	if s.Serial != before {
		t.Fatal("Admit changed state")
	}
	if _, _, err := s.Apply(op, t0); err != nil {
		t.Fatalf("historical acceptance must remain replayable: %v", err)
	}
	op.Milestone = 3
	if err := s.Admit(op); err != nil {
		t.Fatalf("reported step refused: %v", err)
	}
}
