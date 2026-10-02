package core

import "time"

// MilestoneReview is a derived reading of the ordered progress log. It is not
// persisted: a worker's new report supersedes the review of an earlier report.
type MilestoneReview struct {
	Milestone int       `json:"milestone"`
	Label     string    `json:"label"`
	Status    string    `json:"status"`
	By        string    `json:"by,omitempty"`
	At        time.Time `json:"at,omitempty"`
}

// MilestoneReviews reads the latest report or review for each named step.
func (m *Message) MilestoneReviews() []MilestoneReview {
	out := make([]MilestoneReview, len(m.Milestones))
	for i, label := range m.Milestones {
		out[i] = MilestoneReview{Milestone: i + 1, Label: label, Status: "unreviewed"}
	}
	for _, p := range m.Progress {
		if p.Milestone < 1 || p.Milestone > len(out) {
			continue
		}
		r := &out[p.Milestone-1]
		r.Status, r.By, r.At = p.Review, p.By, p.At
		if r.Status == "" {
			r.Status = "unreviewed"
		}
	}
	return out
}
