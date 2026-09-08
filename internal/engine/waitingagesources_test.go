package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// The waiting line reports three kinds of thing, and the age fix reached one.
//
// `waiting` says "N unread message(s), N unacknowledged announcement(s), N
// update(s) to you". The repair that gave it an age took `oldest` from the
// INBOX only, so with no unread mail the line went back to printing identical
// bytes forever: the habituation defect the repair exists to remove, alive on
// two of the three things the line is about. An agent that has been sitting on
// an unacknowledged announcement for six hours was told the same sentence it
// was told six hours ago, which is the state that produced the original
// failure, where a correct notice fired forty times and stopped being read.
//
// Announcements carry MadeAt already. Notices carried no time at all and now
// carry the time of the event that caused them, rather than the time they were
// queued, so that rebuilding the cache after a restart does not report old news
// as fresh.
//
// Written against each SOURCE separately, and deliberately not against the mail
// path, which already worked: a test that seeds mail alongside an announcement
// passes on the mail's age and proves nothing about the announcement.
func TestTheWaitingLineAgesAnnouncementsAndNoticesToo(t *testing.T) {
	now := time.Unix(1700000000, 0)
	long := 3 * time.Hour

	t.Run("announcement with no mail at all", func(t *testing.T) {
		st := core.NewState("t", core.DefaultLimits())
		ap := func(op *core.Op) core.Result {
			r, _, err := st.Apply(op, now)
			if err != nil {
				t.Fatalf("setup %s: %v", op.Kind, err)
			}
			return r
		}
		for _, n := range []string{"speaker", "member"} {
			res := ap(&core.Op{Kind: core.OpRegister, Name: n, NewToken: "tok-" + n})
			id, _ := res["agent_id"].(string)
			ap(&core.Op{Kind: core.OpAckBoard, Token: st.Agents[id].Token})
		}
		ap(&core.Op{Kind: core.OpSpaceOpen, Token: st.Agents["speaker"].Token, Space: "L", Text: "work"})
		ap(&core.Op{Kind: core.OpSpaceJoin, Token: st.Agents["member"].Token, Space: "L"})
		ap(&core.Op{
			Kind: core.OpSpaceAnnounce, Token: st.Agents["speaker"].Token,
			Space: "L", Body: "FREEZE auth/retry.go",
		})

		e := &Engine{state: st}
		// The setup has to be true or the assertion below means nothing: one
		// announcement owed, and no mail to supply an age from.
		if n := len(st.Unacked("member")); n != 1 {
			t.Fatalf("setup: member owes %d announcements, wanted 1", n)
		}
		if n := len(e.pendingMail("member", now)); n != 0 {
			t.Fatalf("setup: %d message(s) in the inbox, wanted none, or the age "+
				"under test could come from the mail", n)
		}
		assertAges(t, e.waiting("member", now), e.waiting("member", now.Add(long)))
	})

	t.Run("notice with no mail and no announcement", func(t *testing.T) {
		st := core.NewState("t", core.DefaultLimits())
		res, _, err := st.Apply(&core.Op{
			Kind: core.OpRegister, Name: "lonely", NewToken: "tok",
		}, now)
		if err != nil {
			t.Fatal("setup:", err)
		}
		id, _ := res["agent_id"].(string)

		e := &Engine{state: st}
		// At is the time the thing HAPPENED, which is what the line reports.
		e.pushNotice(id, "your subagent stopped making progress", 0, now)

		if n := len(e.pendingNotices(id)); n != 1 {
			t.Fatalf("setup: %d notice(s) queued, wanted 1", n)
		}
		if n := len(e.pendingMail(id, now)); n != 0 {
			t.Fatalf("setup: %d message(s) in the inbox, wanted none", n)
		}
		assertAges(t, e.waiting(id, now), e.waiting(id, now.Add(long)))
	})
}

// assertAges is the property both sub-cases share: silent while the thing is
// fresh, and different text once it is not.
//
// Two assertions rather than one. "Says an age at three hours" alone would pass
// against a line that always shouted an age, including for something that
// arrived a moment ago, and spending the novelty of a changing sentence on the
// case that does not need it is exactly how the line went blind before.
func assertAges(t *testing.T, fresh, aged string) {
	t.Helper()
	if fresh == "" || aged == "" {
		t.Fatalf("the line said nothing at all, so there is no nudge to age\n"+
			"  fresh: %q\n  aged:  %q", fresh, aged)
	}
	if strings.Contains(fresh, "has been waiting") {
		t.Errorf("something that just arrived was given an age. Below the floor the "+
			"line stays quiet, or the changing sentence is spent on the case that "+
			"does not need it\n  %s", fresh)
	}
	if !strings.Contains(aged, "has been waiting") {
		t.Errorf("after three hours the line still carries no age, so it reads "+
			"identically at three hours and at three minutes. That is the "+
			"habituation this line was changed to cure\n  %s", aged)
	}
	if fresh == aged {
		t.Errorf("the line is byte-for-byte identical at zero and at three hours, "+
			"so there is a fixed shape for the eye to learn and skip\n  %s", aged)
	}
}
