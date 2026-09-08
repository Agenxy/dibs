package core

import (
	"testing"
	"time"
)

// The purge walked the message map and now emits an event per adoption
// request it expires, so two requests naming the purged mailbox took their
// sub indices from Go's iteration order: the same ledger replayed to the
// same state under a different audit stream. The walk is in serial order.
func TestAPurgeExpiresAdoptionRequestsInSerialOrder(t *testing.T) {
	s := NewState("probe", DefaultLimits())
	now := time.Unix(1700000000, 0)
	ackReg(t, s, "boss", "tok-b", now)
	s.Agents["boss"].Role = RoleCoordinator
	ackReg(t, s, "lost", "tok-l", now)
	s.Agents["lost"].Status = StatusDormant
	var serials []uint64
	for i, name := range []string{"one", "two", "three", "four"} {
		ackReg(t, s, name, "tok-"+name, now)
		req := mustApply(t, s, &Op{
			Kind: OpSendMessage, Token: "tok-" + name, To: "boss", MsgType: MsgRequest,
			Body: "mine", Adopt: "lost", V7Semantics: true,
		}, now.Add(time.Duration(i)*time.Second))
		serials = append(serials, req["msg_serial"].(uint64))
	}
	s.Agents["lost"].Status = StatusArchived
	s.Agents["lost"].ArchivedAt = now.Add(-s.Limits.ArchiveRetention - time.Hour)
	_, evs, err := s.Apply(&Op{Kind: OpSweep, PurgeMail: true, V7Semantics: true}, now.Add(time.Minute))
	if err != nil {
		t.Fatal("setup:", err)
	}
	var got []uint64
	for _, ev := range evs {
		if ev.Type == "message."+MsgStateExpiredDead {
			ms, _ := ev.Data["msg_serial"].(uint64)
			got = append(got, ms)
		}
	}
	if len(got) != len(serials) {
		t.Fatalf("setup: %d expiry event(s) for %d requests: %v", len(got), len(serials), evs)
	}
	for i := range got {
		if got[i] != serials[i] {
			t.Fatalf("the expiry events are in order %v, the requests in %v: a replay of this ledger "+
				"numbers them differently", got, serials)
		}
	}
}
