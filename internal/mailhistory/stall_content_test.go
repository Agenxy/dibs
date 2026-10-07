package mailhistory_test

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

func TestAtomicStallNoticeHasNativeQuotedMailHistory(t *testing.T) {
	f := nativeHistory(t, t.TempDir())
	lead := historyIdentity(t, f, "lead")
	worker := historyIdentity(t, f, "worker")
	reporter := historyOp(t, f, &core.Op{
		Kind: core.OpRegister, Name: "Dibs", Nonce: core.DibsNonce,
		AgentKind: core.KindPersistent, HumanMint: true,
	})["token"].(string)
	request := historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: lead, To: "worker", MsgType: core.MsgRequest, Body: "work"})["msg_serial"].(uint64)
	approved := historyOp(t, f, &core.Op{Kind: core.OpRespond, Token: worker, MsgSerial: request, Disposition: "approve"})
	if approved["state"] != core.MsgStateApproved {
		t.Fatalf("setup approval: %v", approved)
	}
	historyOp(t, f, &core.Op{
		Kind: core.OpStallNotified, Token: reporter, To: "lead", MsgSerial: request,
		DeclarationSerial: request, Body: "PRIVATE-ATOMIC-STALL",
	})
	page := historyJSON(t, settledHistory(t, f, lead, true))
	if !strings.Contains(page, "PRIVATE-ATOMIC-STALL") || !strings.Contains(page, "stall_notified") {
		t.Fatalf("native quoted history lost atomic notice: %s", page)
	}
}
