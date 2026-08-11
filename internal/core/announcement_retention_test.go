package core

import (
	"testing"
	"time"
)

// Settled announcement history is bounded; obligations are not.
//
// Announcements were the one collection in replayed state with no bound. They
// were added on every lane_announce and removed only when an empty auto-opened
// channel was reclaimed, and a standing channel a human opened is never
// reclaimed, so its history grew for the life of the board and was replayed into
// memory on every daemon start. A reviewer made 60 in one channel and found all
// 60 still resident; the visible default of 50 is response pagination, not
// retention.
//
// The distinction this pins is what makes the bound safe. Only fully
// acknowledged announcements may go. An `open` one is something somebody still
// owes, and `unacked` is documented as staying visible forever precisely because
// redelivery gave up on it: evicting either would discard the fact the whole
// mechanism exists to preserve.
func TestSettledAnnouncementsAreBoundedButObligationsAreNot(t *testing.T) {
	s := NewState("n1", DefaultLimits())
	s.Limits.AnnouncementRetention = 3
	now := time.Now()

	s.Channels["standing"] = &Channel{ID: "standing", Members: map[string]*Membership{}}
	add := func(serial uint64, state string) {
		s.Announcements[serial] = &Announcement{
			Serial: serial, Channel: "standing", State: state, MadeAt: now,
		}
	}
	for i := uint64(1); i <= 10; i++ {
		add(i, AnnounceAcked)
	}
	add(11, AnnounceOpen)    // still owed
	add(12, AnnounceUnacked) // redelivery gave up; documented as never dropped

	s.gc(now)

	acked := 0
	for _, a := range s.Announcements {
		if a.State == AnnounceAcked {
			acked++
		}
	}
	if acked != 3 {
		t.Errorf("kept %d settled announcements, want the retention bound of 3", acked)
	}
	// Newest kept, oldest gone: a bound that dropped the recent ones would be
	// worse than none.
	if _, still := s.Announcements[1]; still {
		t.Error("the oldest settled announcement survived; eviction is not oldest-first")
	}
	if _, still := s.Announcements[10]; !still {
		t.Error("the newest settled announcement was evicted")
	}
	if _, still := s.Announcements[11]; !still {
		t.Error("an OPEN announcement was evicted: that is an obligation somebody " +
			"still owes, and losing it silently discharges it")
	}
	if _, still := s.Announcements[12]; !still {
		t.Error("an UNACKED announcement was evicted: it is documented as staying " +
			"visible forever because redelivery gave up on it")
	}
}

// Replaying the same state twice must prune identically, or the ledger stops
// being a fold.
func TestAnnouncementPruningIsDeterministic(t *testing.T) {
	build := func() *State {
		s := NewState("n1", DefaultLimits())
		s.Limits.AnnouncementRetention = 2
		s.Channels["c"] = &Channel{ID: "c", Members: map[string]*Membership{}}
		for i := uint64(1); i <= 6; i++ {
			s.Announcements[i] = &Announcement{
				Serial: i, Channel: "c", State: AnnounceAcked,
			}
		}
		return s
	}
	now := time.Now()
	a, b := build(), build()
	a.gc(now)
	b.gc(now)
	if len(a.Announcements) != len(b.Announcements) {
		t.Fatalf("two identical states pruned differently: %d vs %d",
			len(a.Announcements), len(b.Announcements))
	}
	for serial := range a.Announcements {
		if _, ok := b.Announcements[serial]; !ok {
			t.Errorf("serial %d survived one replay and not the other", serial)
		}
	}
}
