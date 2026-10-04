package core

import "fmt"

// New ingress policy, not a fold rule: old accepted-but-unreported milestones
// remain replayable. A count of reports is not a set of milestone indices.
func (s *State) admitReportedMilestone(op *Op) error {
	if op.Kind != OpRespond || op.Disposition != "accept" || op.Milestone <= 0 {
		return nil
	}
	l := s.AgentByToken(op.Token)
	m := s.Messages[op.MsgSerial]
	if l == nil || m == nil || m.From != l.ID || op.Milestone > len(m.Milestones) {
		return nil // preserve the existing authentication/shape diagnostics
	}
	var reported []int
	for i := 1; i <= len(m.Milestones); i++ {
		for _, p := range m.Progress {
			if p.Review == "" && p.Milestone == i {
				reported = append(reported, i)
				break
			}
		}
	}
	for _, i := range reported {
		if i == op.Milestone {
			return nil
		}
	}
	return errf("E_MILESTONE_UNREPORTED", fmt.Sprintf(
		"reported milestone indices are %v; read_mail(%d) shows their proofs, then "+
			"respond(accept, milestone:<reported index>)", reported, m.Serial),
		"milestone %d has not been reported; reported indices: %v", op.Milestone, reported)
}
