package core

import "fmt"

// New ingress policy, not a fold rule: old accepted-but-unreported milestones
// remain replayable. A count of reports is not a set of milestone indices.
// Done reports the final delivery even if earlier progress had no index; it
// does not invent numbered reports for the intermediate steps.
func (s *State) admitReportedMilestone(op *Op) error {
	if op.Kind != OpRespond || op.Disposition != "accept" || op.Milestone <= 0 {
		return nil
	}
	l := s.AgentByToken(op.Token)
	m := s.Messages[op.MsgSerial]
	if l == nil || m == nil || m.From != l.ID || op.Milestone > len(m.Milestones) {
		return nil // preserve the existing authentication/shape diagnostics
	}
	reported := m.reportedMilestoneIndices()
	for _, i := range reported {
		if i == op.Milestone {
			return nil
		}
	}
	hint := fmt.Sprintf(
		"reported milestone indices are %v; read_mail(%d) shows their proofs, then "+
			"respond(accept, milestone:<reported index>)", reported, m.Serial)
	if len(reported) == 1 {
		hint = fmt.Sprintf("did you mean milestone %d? ", reported[0]) + hint
	}
	return errf("E_MILESTONE_UNREPORTED", hint,
		"milestone %d has not been reported; reported indices: %v", op.Milestone, reported)
}

// The admission decision and its corrective hint read the same unique, ordered
// set. Completion makes only the final index reviewable; it never mutates the
// stored progress log or fabricates intermediate reports.
func (m *Message) reportedMilestoneIndices() []int {
	var reported []int
	for i := 1; i <= len(m.Milestones); i++ {
		if m.State == MsgStateDone && i == len(m.Milestones) {
			reported = append(reported, i)
			continue
		}
		for _, p := range m.Progress {
			if p.Review == "" && p.Milestone == i {
				reported = append(reported, i)
				break
			}
		}
	}
	return reported
}
