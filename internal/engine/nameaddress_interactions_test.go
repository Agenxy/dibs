package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestNameAndIdCannotDisguiseASelfMergeAtTheAdminDoor(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()
	id, worker := regFor(t, e, ctx, "worker")
	_, admin := regFor(t, e, ctx, "admin")
	if _, err := e.GrantRole(ctx, "admin", core.RoleAdmin); err != nil {
		t.Fatal("setup: admin:", err)
	}
	rename(t, e, ctx, worker, "worker-label")
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpSignOff, Token: worker}); err != nil {
		t.Fatal("setup: sign off source so the live-agent refusal cannot mask self-merge:", err)
	}
	var before uint64
	onLoop(t, ctx, e, func(st *core.State) { before = st.Serial })
	_, err := e.MergeAgents(ctx, admin, "worker-label", id)
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_BAD_REQUEST" || !strings.Contains(ce.Msg, "into itself") {
		t.Fatalf("name plus its own ID bypassed admission: %v", err)
	}
	onLoop(t, ctx, e, func(st *core.State) {
		if st.Serial != before || st.Agents[id].MergedInto != "" {
			t.Error("refused self-merge changed replayable state")
		}
	})
}

func TestNameAddressingPreservesReplayedHumanAndCoordinatorRoles(t *testing.T) {
	led := &memLedger{}
	first := New(core.NewState("t", core.DefaultLimits()), led, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go first.Run(ctx)
	human, _, err := first.HumanAgent(ctx)
	if err != nil || human == "" {
		t.Fatal("setup: human:", err)
	}
	_, humanImpostor := regFor(t, first, ctx, "human-impostor")
	rename(t, first, ctx, humanImpostor, "human")
	_, roleImpostor := regFor(t, first, ctx, "role-impostor")
	rename(t, first, ctx, roleImpostor, "coordinator")
	leader, leaderToken := regFor(t, first, ctx, "leader")
	_, sender := regFor(t, first, ctx, "sender")
	if _, err := first.GrantRole(ctx, leader, core.RoleCoordinator); err != nil {
		t.Fatal("setup: role:", err)
	}
	st := replayed(t, ctx, first, led)
	second := New(st, &memLedger{}, deadProber{})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go second.Run(ctx2)
	if second.HumanIdentity() != human {
		t.Fatal("setup: human row was not recovered from replay")
	}
	for _, target := range []struct{ role, id string }{{"human", human}, {"coordinator", leader}} {
		op := &core.Op{Kind: core.OpSendMessage, Token: sender, To: target.role, MsgType: core.MsgNotify, Body: "for the role"}
		if _, err := second.Do(ctx2, op); err != nil {
			t.Fatalf("send to replayed %s role: %v", target.role, err)
		}
		if op.To != target.id {
			t.Errorf("%s role reached %s instead of %s", target.role, op.To, target.id)
		}
	}
	// Name resolution must run before the mailbox guard, even for a row
	// recovered from history. A coordinator cannot take the person's mail.
	recoveredID, humanToken, err := second.HumanAgent(ctx2)
	if err != nil || recoveredID != human || humanToken == "" {
		t.Fatal("setup: recovered human credential:", err)
	}
	rename(t, second, ctx2, humanToken, "operator-label")
	humanLabel := "operator-label"
	for _, op := range []*core.Op{
		{Kind: core.OpAdoptAgent, Token: leaderToken, To: humanLabel},
		{Kind: core.OpMergeAgents, To: humanLabel, MergeInto: "sender"},
	} {
		if op.Kind == core.OpMergeAgents {
			if _, err := second.MergeAgents(ctx2, sender, humanLabel, "sender"); err == nil {
				t.Fatal("a member merged the replayed human's mailbox")
			}
			continue
		}
		_, err := second.Do(ctx2, op)
		if err == nil || !strings.Contains(err.Error(), "E_NOT_PERMITTED") {
			t.Fatalf("name-addressed human adoption: %v", err)
		}
	}
}

func TestRenameKeepsQueueSerialOwnershipAndContactIncarnation(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()
	id, tok := regFor(t, e, ctx, "worker")
	_, sender := regFor(t, e, ctx, "sender")
	rename(t, e, ctx, tok, "new-label")
	r, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "new-label", MsgType: core.MsgRequest, Body: "later"})
	if err != nil {
		t.Fatal("setup: send by current label:", err)
	}
	n := r["msg_serial"].(uint64)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRespond, Token: tok, MsgSerial: n, Disposition: "queue"}); err != nil {
		t.Fatal("queue by serial:", err)
	}
	rename(t, e, ctx, tok, "later-label")
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpQueueUpdate, Token: tok, MsgSerial: n, QueuePriority: "urgent"}); err != nil {
		t.Fatal("update by serial after rename:", err)
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: tok}); err != nil {
		t.Fatal("authenticated contact after rename:", err)
	}
	onLoop(t, ctx, e, func(st *core.State) {
		q := st.TaskQueue(id)
		if len(q) != 1 || q[0].Serial != n || q[0].To != id || q[0].EffectivePriority() != "urgent" {
			t.Errorf("queue ownership changed with label: %+v", q)
		}
		if len(st.TaskQueue("later-label")) != 0 {
			t.Error("a mutable label became a queue owner")
		}
		c, ok := e.contact[id]
		if !ok || c.at.IsZero() || c.created != st.Agents[id].CreatedSerial {
			t.Error("actual authenticated call lost contact for stable incarnation")
		}
		for _, label := range []string{"new-label", "later-label"} {
			if _, ok := e.contact[label]; ok {
				t.Error("contact cache keyed by mutable label:", label)
			}
		}
	})
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpQueueUpdate, Token: sender, MsgSerial: n, QueueTail: true}); err == nil {
		t.Fatal("sender acquired recipient's queue by rename")
	}
}

func TestMailboxNameResolutionRunsAfterBothAuthorizationGates(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()
	_, member := regFor(t, e, ctx, "member")
	_, lead := regFor(t, e, ctx, "lead")
	_, admin := regFor(t, e, ctx, "admin")
	for _, role := range []struct{ id, role string }{{"lead", core.RoleCoordinator}, {"admin", core.RoleAdmin}} {
		if _, err := e.GrantRole(ctx, role.id, role.role); err != nil {
			t.Fatal("setup: grant:", err)
		}
	}
	// Historical rows can retain the same label after its original ID was
	// purged. The production read must gate this fixture before resolving it.
	onLoop(t, ctx, e, func(st *core.State) {
		for _, id := range []string{"shared-2", "shared-3"} {
			st.Agents[id] = &core.Agent{ID: id, Name: "shared", Status: core.StatusActive}
		}
	})
	for _, tc := range []struct {
		token  string
		census bool
		want   string
	}{{member, true, "E_NOT_COORDINATOR"}, {lead, false, "E_NOT_ADMIN"}, {lead, true, "E_AMBIGUOUS_AGENT"}, {admin, false, "E_AMBIGUOUS_AGENT"}} {
		_, err := e.AllMail(ctx, tc.token, tc.census, "shared")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("census=%v: got %v, want %s", tc.census, err, tc.want)
		}
	}
	id, worker := regFor(t, e, ctx, "worker")
	rename(t, e, ctx, worker, "mail-label")
	for _, to := range []string{id, "member"} {
		if _, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: member, To: to, MsgType: core.MsgNotify, Body: "private"}); err != nil {
			t.Fatal("setup: mail:", err)
		}
	}
	r, err := e.AllMail(ctx, admin, false, "mail-label")
	if err != nil {
		t.Fatal("authorized full mailbox by name:", err)
	}
	mail, ok := r["messages"].([]*core.Message)
	if !ok || len(mail) != 1 || mail[0].To != id {
		t.Fatalf("name selector returned other mailboxes: %+v", mail)
	}
}
