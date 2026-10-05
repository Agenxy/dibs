package core

import (
	"errors"
	"fmt"
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
	var problem *Error
	if !errors.As(err, &problem) || !strings.HasPrefix(problem.Hint, "did you mean milestone 3?") {
		t.Errorf("single reported step is not named first: %v", err)
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

// Done is the worker's final report even when its progress notes had no
// numbered step. It does not invent reports for earlier intermediate steps.
func TestDoneReportsTheFinalMilestoneForAdmission(t *testing.T) {
	for _, note := range []bool{false, true} {
		t.Run(fmt.Sprintf("progress-note-%t", note), func(t *testing.T) {
			s, serial := taskRequest(t, "intermediate proof", "final delivery")
			if note {
				mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "progress", Body: "delivery being prepared"}, t0)
			}
			op := &Op{Kind: OpRespond, Token: "tl", MsgSerial: serial, Disposition: "accept", Milestone: 2}
			if err := s.Admit(op); codeOf(err) != "E_MILESTONE_UNREPORTED" {
				t.Fatalf("unfinished final milestone was accepted: %v", err)
			}
			mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "done", Body: "final work delivered", Deliverable: "artifact:final"}, t0)
			before := s.Serial
			if err := s.Admit(op); err != nil {
				t.Errorf("DONE final milestone falsely refused: %v", err)
			}
			op.Milestone = 1
			if err := s.Admit(op); codeOf(err) != "E_MILESTONE_UNREPORTED" {
				t.Errorf("DONE invented an intermediate milestone report: %v", err)
			}
			if s.Serial != before || s.Messages[serial].Reached() != 0 {
				t.Fatal("admission changed state or invented numbered progress")
			}
		})
	}
}
