// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package core

import (
	"errors"
	"testing"
	"time"
)

// taskRequest is approvedRequest with milestones.
func taskRequest(t *testing.T, steps ...string) (*State, uint64) {
	t.Helper()
	s := NewState("n1", DefaultLimits())
	reg(t, s, "lead", "tl", t0)
	reg(t, s, "worker", "tw", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tl"}, t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tw"}, t0)
	res := mustApply(t, s, &Op{
		Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgRequest,
		Body: "write the report", Milestones: steps, OpID: "t1",
	}, t0)
	serial, _ := res["msg_serial"].(uint64)
	if serial == 0 {
		t.Fatalf("setup: no serial in %v", res)
	}
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "approve"}, t0)
	return s, serial
}

func codeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// The sender follows the work without asking: each report reaches it as an
// event, milestones count once however often they are reported, and done
// says where the work landed. Asked for by the operator (Dibs #7424).
func TestATaskIsFollowedToItsDeliverable(t *testing.T) {
	s, serial := taskRequest(t, "sources gathered", "draft written", "summary sent")

	_, evs, err := s.Apply(&Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "progress", Milestone: 1, Body: "12 sources"}, t0)
	if err != nil {
		t.Fatalf("progress on an approved task: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "message.progress" || evs[0].To != "lead" {
		t.Fatalf("events %+v: the sender is the one following this", evs)
	}
	if evs[0].Data["label"] != "sources gathered" || evs[0].Data["reached"] != 1 || evs[0].Data["total"] != 3 {
		t.Errorf("the event says %v, want sources gathered, 1 of 3", evs[0].Data)
	}
	if IsMailEvent("message.progress") || Blocking("message.progress", "") {
		t.Error("progress wakes the sender: it is news about a task, and done is the one somebody waits on")
	}
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "progress", Milestone: 1}, t0)
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "progress", Body: "halfway through the draft"}, t0)
	m := s.Messages[serial]
	if m.Reached() != 1 || len(m.Progress) != 3 {
		t.Errorf("reached %d with %d reports, want 1 milestone over 3 reports", m.Reached(), len(m.Progress))
	}
	if m.State != MsgStateApproved {
		t.Errorf("progress moved the request to %q: it is still owed", m.State)
	}

	_, evs, err = s.Apply(&Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "done", Deliverable: "/reports/q3.md"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if m.Deliverable != "/reports/q3.md" || evs[0].Data["deliverable"] != "/reports/q3.md" {
		t.Errorf("the deliverable is %q on the message and %v on the event", m.Deliverable, evs[0].Data["deliverable"])
	}
	if _, _, err := s.Apply(&Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "progress", Body: "late"}, t0); codeOf(err) != "E_BAD_DISPOSITION" {
		t.Errorf("progress after done gave %v", err)
	}
}

// Progress is reported against work the recipient took on, and only steps
// the sender named.
func TestProgressIsOnlyForWorkYouApproved(t *testing.T) {
	s, serial := taskRequest(t, "one", "two")
	for name, op := range map[string]*Op{
		"a step that does not exist": {Milestone: 3},
		"an empty report":            {},
	} {
		op.Kind, op.Token, op.MsgSerial, op.Disposition = OpRespond, "tw", serial, "progress"
		if _, _, err := s.Apply(op, t0); codeOf(err) != "E_BAD_ARG" {
			t.Errorf("%s gave %v, want E_BAD_ARG", name, err)
		}
	}
	pending := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgRequest, Body: "x", OpID: "p"}, t0)["msg_serial"].(uint64)
	if _, _, err := s.Apply(&Op{Kind: OpRespond, Token: "tw", MsgSerial: pending, Disposition: "progress", Body: "on it"}, t0); codeOf(err) != "E_BAD_DISPOSITION" {
		t.Errorf("progress on a request not yet approved gave %v", err)
	}
}

// The shapes, refused at ingress: each field belongs to one call.
func TestTaskFieldsBelongWhereTheyMeanSomething(t *testing.T) {
	for name, op := range map[string]*Op{
		"milestones on a question": {Kind: OpSendMessage, MsgType: MsgQuestion, Milestones: []string{"a"}},
		"milestones on a grant":    {Kind: OpSendMessage, MsgType: MsgRequest, Grant: RoleCoordinator, Milestones: []string{"a"}},
		"a blank milestone":        {Kind: OpSendMessage, MsgType: MsgRequest, Milestones: []string{" "}},
		"too many milestones":      {Kind: OpSendMessage, MsgType: MsgRequest, Milestones: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}},
		"a milestone on approve":   {Kind: OpRespond, Disposition: "approve", Milestone: 1},
		"a deliverable on approve": {Kind: OpRespond, Disposition: "approve", Deliverable: "/x"},
		"a negative milestone":     {Kind: OpRespond, Disposition: "progress", Milestone: -1},
	} {
		if err := checkTask(op); err == nil {
			t.Errorf("%s was admitted", name)
		}
	}
	if err := checkTask(&Op{Kind: OpSendMessage, MsgType: MsgRequest, Milestones: []string{"a", "b"}}); err != nil {
		t.Errorf("a request with milestones was refused: %v", err)
	}
}

// The requester checks a step's artifact before the work is done and says
// what it thinks, without cancelling anything; the worker is told, and a
// flag needs a reason (Dibs #7456).
func TestTheRequesterReviewsAStepWithoutCancellingTheTask(t *testing.T) {
	s, serial := taskRequest(t, "sources gathered", "draft written")
	_, evs, err := s.Apply(&Op{
		Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "progress",
		Milestone: 1, Deliverable: "/work/sources.md",
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if evs[0].Data["artifact"] != "/work/sources.md" {
		t.Errorf("the step's artifact did not travel to the requester: %v", evs[0].Data)
	}

	_, evs, err = s.Apply(&Op{
		Kind: OpRespond, Token: "tl", MsgSerial: serial, Disposition: "flag",
		Milestone: 1, Body: "two of these are paywalled, find open ones",
	}, t0)
	if err != nil {
		t.Fatalf("the requester could not flag a step: %v", err)
	}
	if evs[0].Type != "message.review" || evs[0].To != "worker" || evs[0].Data["review"] != ReviewFlagged {
		t.Errorf("events %+v: the flag is for the worker", evs)
	}
	m := s.Messages[serial]
	if m.State != MsgStateApproved {
		t.Errorf("a flag moved the task to %q: it is still the worker's", m.State)
	}
	if m.Reached() != 1 {
		t.Errorf("a review counted as a step reached: %d", m.Reached())
	}
	if _, _, err := s.Apply(&Op{Kind: OpRespond, Token: "tl", MsgSerial: serial, Disposition: "flag", Milestone: 1}, t0); codeOf(err) != "E_BAD_ARG" {
		t.Errorf("a flag with no reason gave %v", err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "accept", Milestone: 1}, t0); codeOf(err) != "E_NO_MESSAGE" {
		t.Errorf("the WORKER accepted its own step: %v", err)
	}
	if _, _, err := s.Apply(&Op{Kind: OpRespond, Token: "tl", MsgSerial: serial, Disposition: "accept", Milestone: 1}, t0); err != nil {
		t.Errorf("the requester could not accept a step: %v", err)
	}
}

// The worker often knows the steps: it may name them as it approves a
// request that named none, and not rewrite steps the requester named.
func TestTheWorkerDeclaresTheStepsWhenTheRequesterNamedNone(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	reg(t, s, "lead", "tl", t0)
	reg(t, s, "worker", "tw", t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tl"}, t0)
	mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tw"}, t0)
	plain := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgRequest, Body: "x", OpID: "a"}, t0)["msg_serial"].(uint64)
	mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: plain, Disposition: "approve", Milestones: []string{"one", "two"}}, t0)
	if got := s.Messages[plain].Milestones; len(got) != 2 {
		t.Fatalf("the worker's milestones were not kept: %v", got)
	}
	named := mustApply(t, s, &Op{Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgRequest, Body: "y", OpID: "b", Milestones: []string{"mine"}}, t0)["msg_serial"].(uint64)
	if _, _, err := s.Apply(&Op{Kind: OpRespond, Token: "tw", MsgSerial: named, Disposition: "approve", Milestones: []string{"theirs"}}, t0); codeOf(err) != "E_BAD_ARG" {
		t.Errorf("the worker rewrote the requester's steps: %v", err)
	}
	if s.Messages[named].State != MsgStateApproved && s.Messages[named].State != MsgStateDelivered && s.Messages[named].State != MsgStatePending {
		t.Errorf("state %q", s.Messages[named].State)
	}
}

// A tracked request outlives the sweep for its task's lifetime, finished or
// not, so a host polling tasks/get after done still gets the result; an
// untracked one goes as it always did.
func TestATrackedRequestIsKeptForItsTask(t *testing.T) {
	keep := func(track bool) bool {
		s := NewState("n1", DefaultLimits())
		reg(t, s, "lead", "tl", t0)
		reg(t, s, "worker", "tw", t0)
		mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tl"}, t0)
		mustApply(t, s, &Op{Kind: OpAckBoard, Token: "tw"}, t0)
		serial := mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "tl", To: "worker", MsgType: MsgRequest,
			Body: "x", OpID: "k", Track: track,
		}, t0)["msg_serial"].(uint64)
		mustApply(t, s, &Op{Kind: OpRespond, Token: "tw", MsgSerial: serial, Disposition: "deny"}, t0)
		mustApply(t, s, &Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true, KeepOwed: true}, t0.Add(2*time.Hour))
		_, kept := s.Messages[serial]
		return kept
	}
	if !keep(true) {
		t.Error("a tracked request was swept two hours after it finished, under a task that lives a week")
	}
	if keep(false) {
		t.Error("an untracked request was kept: tracking is opt-in")
	}
}
