package core

import (
	"strings"
	"time"
)

// A request is a TASK its sender can follow without asking.
//
// approve said "I will" and done said "I have", and between the two the
// sender saw nothing: an agent waiting on a report armed a file watcher on
// the path it expected, because nothing would tell it where the output
// landed or how far along it was. Asked for by the operator, relayed by
// agenxy-supply (Dibs #7424): a request may name milestones, the recipient
// reports progress against them, and done may say where the work is.
//
// No new message type. A task is a request with steps, so it keeps the
// lifecycle every harness already handles, and a request with no milestones
// is exactly what it was.

// Progress is one report against an approved request.
//
// Two hands write here. The recipient reports (Review empty): a step reached,
// a note, and optionally an artifact the sender can open and check before the
// work is done. The sender reviews (Review "accepted" or "flagged"): agreeing
// with a step, or raising a problem or a change of direction, without
// cancelling anything.
type Progress struct {
	Milestone int       `json:"milestone,omitempty"` // 1-based; 0 is a note with no step
	Note      string    `json:"note,omitempty"`
	Artifact  string    `json:"artifact,omitempty"` // a path or URL to check this step by
	Review    string    `json:"review,omitempty"`   // "", accepted or flagged
	By        string    `json:"by,omitempty"`       // the agent that wrote this entry
	Serial    uint64    `json:"serial"`
	At        time.Time `json:"at"`
}

// Review verdicts a sender gives a step.
const (
	ReviewAccepted = "accepted"
	ReviewFlagged  = "flagged"
)

// TaskTTL is how long a tracked request is kept from when it was sent, and
// the ttlMs its MCP task states: a week, because work worth tracking is work
// that takes a while, and a task that vanishes before its sender polls has
// told it nothing.
const TaskTTL = 7 * 24 * time.Hour

// MaxMilestones bounds the steps one request may name: enough for a real
// plan, few enough that the board can show them as a count.
const MaxMilestones = 8

// MaxMilestoneBytes bounds one step's label. Longer than a name, because
// "report at <path>" is a reasonable step.
const MaxMilestoneBytes = 200

// MaxDeliverableBytes bounds a deliverable: a path or a URL.
const MaxDeliverableBytes = 1024

// maxProgressReports bounds the reports one request keeps, so a recipient
// reporting in a loop grows a message by this much and no more.
const maxProgressReports = 64

// checkTask is the shape-only half of tasks, at ingress where validation
// belongs: what each field may be attached to and how big it may be.
func checkTask(op *Op) error {
	if err := checkMilestones(op); err != nil {
		return err
	}
	return checkTaskRefs(op)
}

// checkMilestones: steps are named by the sender on a request for work, or by
// the recipient as it approves one that named none.
func checkMilestones(op *Op) error {
	if len(op.Milestones) == 0 {
		return nil
	}
	onSend := op.Kind == OpSendMessage && op.MsgType == MsgRequest
	onApprove := op.Kind == OpRespond && (op.Disposition == "approve" || op.Disposition == "queue")
	if !onSend && !onApprove {
		return errf("E_BAD_TYPE",
			`milestones name the steps of work: send them on a "request", or declare them `+
				`as you approve one with respond(approve, milestones)`,
			"milestones on a %s %s", op.Kind, op.MsgType+op.Disposition)
	}
	if op.Grant != "" || op.Adopt != "" {
		return errf("E_BAD_ARG",
			"a request that grants or adopts is performed by approving it, so it has no "+
				"steps to report: send the milestones on a request for work",
			"milestones on a request that carries its own effect")
	}
	if len(op.Milestones) > MaxMilestones {
		return errTooLarge("milestones", MaxMilestones)
	}
	for _, m := range op.Milestones {
		if strings.TrimSpace(m) == "" {
			return errf("E_BAD_ARG", "give every milestone a label: the recipient ticks it by "+
				"number, and you read it by name", "a blank milestone")
		}
		if len(m) > MaxMilestoneBytes {
			return errTooLarge("milestone", MaxMilestoneBytes)
		}
	}
	return nil
}

// checkTaskRefs: a milestone number goes with a report or a review, and a
// deliverable with a report or with done.
func checkTaskRefs(op *Op) error {
	isRespond := op.Kind == OpRespond
	switch {
	case op.Track && (op.Kind != OpSendMessage || op.MsgType != MsgRequest):
		return errf("E_BAD_ARG", `track follows a request as a task: send it on a "request"`,
			"track given on a %s", op.MsgType)
	case op.Milestone < 0:
		return errf("E_BAD_ARG", "milestones are numbered from 1; 0 or none is a note with no step",
			"milestone %d", op.Milestone)
	case op.Milestone != 0 && (!isRespond || !reviewsOrReports(op.Disposition)):
		return errf("E_BAD_ARG", "milestone is the step a respond(progress), (accept) or (flag) is about",
			"milestone given with %s", op.Disposition)
	case op.Deliverable != "" && (!isRespond || (op.Disposition != "done" && op.Disposition != "progress")):
		return errf("E_BAD_ARG", `deliverable says where work landed: give it with respond(done), `+
			`or with respond(progress) for a step's artifact`, "deliverable given with %s", op.Disposition)
	case len(op.Deliverable) > MaxDeliverableBytes:
		return errTooLarge("deliverable", MaxDeliverableBytes)
	}
	return nil
}

func reviewsOrReports(disposition string) bool {
	switch disposition {
	case "progress", "accept", "flag":
		return true
	}
	return false
}

// applyProgress records a report against a request the recipient approved.
// State rules, so in the fold: they read the message. "progress" was an
// unknown disposition to every earlier build, so no ledger holds one.
func (s *State) applyProgress(m *Message, op *Op, now time.Time) (Result, []Event, error) {
	if !m.canReportProgress() {
		state := m.State
		if state == "" {
			state = "pending"
		}
		return nil, nil, errf("E_BAD_DISPOSITION",
			"progress reports on an approved request, or a completed request with an unresolved "+
				"review flag: approve first, and close delivered work with done",
			"message %d is a %s and %s", m.Serial, m.Type, state)
	}
	if op.Milestone > len(m.Milestones) {
		hint := "this request named no milestones: report a note with no milestone"
		if len(m.Milestones) > 0 {
			hint = "its milestones are numbered 1 to " + itoa(len(m.Milestones)) + "; read_mail lists them"
		}
		return nil, nil, errf("E_BAD_ARG", hint, "message %d has no milestone %d", m.Serial, op.Milestone)
	}
	if op.Milestone == 0 && strings.TrimSpace(op.Body) == "" && op.Deliverable == "" {
		return nil, nil, errf("E_BAD_ARG", "say what moved: a note, a milestone, an artifact, or several",
			"an empty progress report")
	}
	if len(op.Body) > s.Limits.MaxBodyBytes {
		return nil, nil, errTooLarge("progress note", s.Limits.MaxBodyBytes)
	}
	if len(m.Progress) >= maxProgressReports {
		return nil, nil, errf("E_QUOTA",
			"this request has all the progress reports it can hold: close it with done, or "+
				"send the sender a notify", "message %d has %d reports", m.Serial, len(m.Progress))
	}
	m.Progress = append(m.Progress, Progress{
		Milestone: op.Milestone, Note: op.Body, Artifact: op.Deliverable, By: m.To,
		Serial: s.Serial + 1, At: now,
	})
	reached, total := m.Reached(), len(m.Milestones)
	data := map[string]any{"msg_serial": m.Serial, "reached": reached, "total": total}
	if op.Deliverable != "" {
		data["artifact"] = op.Deliverable
	}
	if op.Milestone > 0 {
		data["milestone"] = op.Milestone
		data["label"] = m.Milestones[op.Milestone-1]
	}
	evs := []Event{{Type: "message.progress", Agent: m.To, To: m.From, Data: data}}
	s.finish(&evs, now)
	return Result{"ok": true, "state": m.State, "reached": reached, "total": total}, evs, nil
}

// Done work permits correction reports only while a review flag remains.
// Approval requests that grant roles or move mailboxes never report progress.
func (m *Message) canReportProgress() bool {
	if m.Type != MsgRequest || m.Grant != "" || m.Adopt != "" {
		return false
	}
	return m.State == MsgStateApproved || (m.State == MsgStateDone && m.HasUnresolvedReviewFlags())
}

// Reached counts the distinct milestones the recipient reported, however
// many times each. A sender's review is not a step reached.
func (m *Message) Reached() int {
	seen := map[int]bool{}
	for _, p := range m.Progress {
		if p.Milestone > 0 && p.Review == "" {
			seen[p.Milestone] = true
		}
	}
	return len(seen)
}

// applyReview records the SENDER's verdict on a step: accepted, or flagged
// with what is wrong or what to do instead. Nothing is cancelled; the task
// stays with its recipient, who is told. Asked for by the operator (Dibs
// #7456): a requester checks partial results along the way and can redirect
// without starting over.
func (s *State) applyReview(l *Agent, op *Op, now time.Time) (Result, []Event, error) {
	m, ok := s.Messages[op.MsgSerial]
	if !ok || m.From != l.ID || m.Type != MsgRequest {
		return nil, nil, errf("E_NO_MESSAGE",
			"accept and flag review the steps of a request YOU sent: read_mail on it shows its "+
				"milestones and what was reported",
			"no request %d sent by you", op.MsgSerial)
	}
	if m.State != MsgStateApproved && m.State != MsgStateDone {
		return nil, nil, errf("E_BAD_DISPOSITION",
			"there is nothing to review until the recipient approves it and reports",
			"message %d is %s", m.Serial, m.State)
	}
	if op.Milestone > len(m.Milestones) {
		return nil, nil, errf("E_BAD_ARG", "its milestones are numbered 1 to "+itoa(len(m.Milestones)),
			"message %d has no milestone %d", m.Serial, op.Milestone)
	}
	review := ReviewAccepted
	if op.Disposition == "flag" {
		review = ReviewFlagged
		if strings.TrimSpace(op.Body) == "" {
			return nil, nil, errf("E_BAD_ARG", "say what is wrong, or what to do instead: that is "+
				"the whole of a flag", "a flag with no body")
		}
	} else if op.Milestone == 0 && !m.unresolvedReviewFlags()[0] {
		return nil, nil, errf("E_BAD_ARG", "accept names the milestone you checked", "accept with no milestone")
	}
	if len(op.Body) > s.Limits.MaxBodyBytes {
		return nil, nil, errTooLarge("review note", s.Limits.MaxBodyBytes)
	}
	if len(m.Progress) >= maxProgressReports {
		return nil, nil, errf("E_QUOTA", "this task holds all the entries it can: send the recipient a message",
			"message %d has %d entries", m.Serial, len(m.Progress))
	}
	m.Progress = append(m.Progress, Progress{
		Milestone: op.Milestone, Note: op.Body, Review: review, By: l.ID,
		Serial: s.Serial + 1, At: now,
	})
	data := map[string]any{"msg_serial": m.Serial, "review": review}
	if op.Milestone > 0 {
		data["milestone"], data["label"] = op.Milestone, m.Milestones[op.Milestone-1]
	}
	evs := []Event{{Type: "message.review", Agent: l.ID, To: m.To, Data: data}}
	s.finish(&evs, now)
	return Result{"ok": true, "review": review}, evs, nil
}

// declareMilestones lets the recipient name the steps as it approves, when
// the sender named none: it is often the one who knows what they are.
func declareMilestones(m *Message, op *Op, st string) error {
	if len(op.Milestones) == 0 || (st != MsgStateApproved && st != MsgStateQueued) {
		return nil
	}
	if len(m.Milestones) > 0 {
		return errf("E_BAD_ARG", "its sender already named the steps: report against those, "+
			"and send the sender a message if they should change",
			"message %d already has milestones", m.Serial)
	}
	m.Milestones = op.Milestones
	return nil
}
