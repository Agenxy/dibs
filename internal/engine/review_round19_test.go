package engine

import (
	"context"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// R19-2: a role a person set stands against a plain regrant for the rest of
// the run, and the decision is made on the loop with the grant itself.
func TestAHumanRoleChangeStandsAgainstARegrant(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "lead", AgentKind: core.KindPersistent, Nonce: "n-lead-0123456789abcdef"}); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.GrantRole(ctx, "lead", core.RoleAdmin); err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.GrantRoleByHuman(ctx, "lead", core.RoleMember); err != nil {
		t.Fatal(err)
	}
	res, err := e.GrantRole(ctx, "lead", core.RoleAdmin) // the reconciler's tick
	if err != nil {
		t.Fatal(err)
	}
	if res["stands"] == nil || st.Agents["lead"].Role == core.RoleAdmin {
		t.Fatalf("a regrant after a person's demotion went through (%v, role %q): the predecessor "+
			"keeps admin through the handover", res, st.Agents["lead"].Role)
	}
}

// R20-1: a role set by a person's approval of a grant request stands
// against a regrant, as one set through the admin API does.
func TestAnApprovedGrantStandsAgainstARegrant(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	humanID, humanTok, err := e.HumanAgent(ctx)
	if err != nil {
		t.Fatal("setup:", err)
	}
	lead, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "lead", AgentKind: core.KindPersistent, Nonce: "n-lead-0123456789abcdef"})
	if err != nil {
		t.Fatal("setup:", err)
	}
	if _, err := e.GrantRole(ctx, "lead", core.RoleAdmin); err != nil {
		t.Fatal("setup:", err)
	}
	ask, err := e.Do(ctx, &core.Op{Kind: core.OpSendMessage, Token: lead["token"].(string), To: humanID, MsgType: core.MsgRequest, Body: "let me step down", Grant: core.RoleMember})
	if err != nil {
		t.Fatal("setup:", err)
	}
	serial, _ := ask["msg_serial"].(uint64)
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRespond, Token: humanTok, MsgSerial: serial, Disposition: "approve", Body: "ok"}); err != nil {
		t.Fatal("setup: the human's approval failed:", err)
	}
	if st.Agents["lead"].Role != core.RoleMember {
		t.Fatalf("setup: the approval did not change the role (%q)", st.Agents["lead"].Role)
	}
	res, err := e.GrantRole(ctx, "lead", core.RoleAdmin) // the reconciler's tick
	if err != nil {
		t.Fatal(err)
	}
	if res["stands"] == nil || st.Agents["lead"].Role == core.RoleAdmin {
		t.Fatalf("a regrant after a person approved the demotion went through (%v, role %q)", res, st.Agents["lead"].Role)
	}
}

// R24-2: a human grant that failed records nothing; the configured grant the
// agent is owed when it registers still applies.
func TestAFailedHumanGrantDoesNotSuppressTheConfiguredOne(t *testing.T) {
	st := core.NewState("t", core.DefaultLimits())
	e := New(st, &memLedger{}, deadProber{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if _, err := e.GrantRoleByHuman(ctx, "fleet-lead", core.RoleCoordinator); err == nil {
		t.Fatal("setup: a grant to a name nobody has registered succeeded")
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "fleet-lead", AgentKind: core.KindPersistent, Nonce: "n-fl-0123456789abcdef"}); err != nil {
		t.Fatal("setup:", err)
	}
	res, err := e.GrantRole(ctx, "fleet-lead", core.RoleCoordinator) // the reconciler's grant
	if err != nil {
		t.Fatal(err)
	}
	if res["stands"] != nil || st.Agents["fleet-lead"].Role != core.RoleCoordinator {
		t.Fatalf("the configured grant was skipped after a human grant that never applied (%v, role %q)", res, st.Agents["fleet-lead"].Role)
	}
}
