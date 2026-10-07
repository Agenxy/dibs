package mailhistory_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mailhistory"
)

func historyUnits(t *testing.T, res core.Result) []core.Result {
	t.Helper()
	rows, ok := res["units"].([]core.Result)
	if !ok {
		t.Fatal("expected authorized metadata rows:", res)
	}
	return rows
}

func reviewLedger(t *testing.T, dir string, limits core.Limits) (*ledger.Ledger, *historyBudgetWriter) {
	t.Helper()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup:", err)
	}
	l, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "history-native", box)
	if err != nil {
		t.Fatal("setup:", err)
	}
	st := core.NewState("history-native", limits)
	w := &historyBudgetWriter{t: t, state: st, ledger: l, now: time.Now().UTC().Add(-time.Minute)}
	for _, id := range []string{"sender", "recipient", "heir", "admin"} {
		w.apply(&core.Op{Kind: core.OpRegister, Name: id, NewToken: id + "-token", Nonce: id + "-nonce", PID: 1, AgentKind: core.KindPersistent})
		w.apply(&core.Op{Kind: core.OpAckBoard, Token: id + "-token"})
	}
	return l, w
}

func TestMailHistoryNativeAdoptionSourceLimitIsPartyLocalAfterRestart(t *testing.T) {
	dir := t.TempDir()
	limits := core.DefaultLimits()
	limits.MaxAgents, limits.MaxPersistentAgents, limits.MaxMailboxDepth = 512, 512, 512
	l, w := reviewLedger(t, dir, limits)
	w.apply(&core.Op{Kind: core.OpGrantRole, To: "admin", Mode: core.RoleAdmin})
	for n := 0; n < 300; n++ {
		id := "source-" + strconv.Itoa(n)
		w.apply(&core.Op{Kind: core.OpRegister, Name: id, NewToken: id + "-token", Nonce: id + "-nonce", PID: 1, AgentKind: core.KindPersistent})
		w.apply(&core.Op{Kind: core.OpAckBoard, Token: id + "-token"})
		w.apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: id, MsgType: core.MsgNotify, Body: "bounded source evidence"})
		w.apply(&core.Op{Kind: core.OpSweep, DeadAgents: []string{id}})
		res := w.apply(&core.Op{Kind: core.OpAdoptAgent, Token: "admin-token", To: id, Space: "heir", AdoptAuthorised: true})
		if res["messages"] != 1 {
			t.Fatal("setup: actual adoption:", res)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal("setup:", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		f := nativeHistoryWithLimits(t, dir, limits)
		f.ids["sender-token"], f.ids["heir-token"] = "sender", "heir"
		timeout := time.NewTimer(5 * time.Second)
		tick := time.NewTicker(time.Millisecond)
		for !f.led.MailHistory().Measurement().Ready {
			if f.led.MailHistory().Status().Failed {
				t.Fatal("unrelated party lost history after adoption source overflow")
			}
			select {
			case <-tick.C:
			case <-timeout.C:
				t.Fatal("setup: adoption history never built")
			}
		}
		timeout.Stop()
		tick.Stop()
		res := settledHistory(t, f, "sender-token", false)
		if len(historyUnits(t, res)) == 0 {
			t.Fatal("unrelated party lost history after adoption source overflow", res)
		}
		denied, err := any(f.eng).(historyAPI).ReadMailHistory(f.ctx, "heir-token", 0, 25, false, "")
		var ce *core.Error
		if !errors.As(err, &ce) || ce.Code != "E_HISTORY_UNAVAILABLE" || !strings.Contains(ce.Hint, "retry cannot restore") {
			t.Fatal("overflow party lacked honest permanent scoped refusal:", err, denied)
		}
		f.stop()
	}
}

func forgedHistoryCursor(t *testing.T, value string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal("setup: native cursor:", err)
	}
	var fields map[string]any
	var suffix []byte
	// Identical fixture accepts each source's original cursor envelope. Only
	// the fixed upper field is altered; an existing MAC is left unchanged.
	if json.Unmarshal(raw, &fields) != nil {
		if len(raw) < 16 {
			t.Fatal("setup: cursor envelope")
		}
		suffix = raw[len(raw)-16:]
		if err := json.Unmarshal(raw[:len(raw)-16], &fields); err != nil {
			t.Fatal("setup:", err)
		}
	}
	fields["u"] = fields["u"].(float64) - 1
	modified, err := json.Marshal(fields)
	if err != nil {
		t.Fatal("setup:", err)
	}
	return base64.RawURLEncoding.EncodeToString(append(modified, suffix...))
}

func assertInvalidReviewCursor(t *testing.T, f nativeHistoryFixture, token, value, marker string) {
	t.Helper()
	res, err := any(f.eng).(historyAPI).ReadMailHistory(f.ctx, token, 0, 1, false, value)
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != "E_HISTORY_CURSOR" {
		t.Fatal(marker, err, res)
	}
}

func TestMailHistoryNativeCursorForgeryAndForeignReplayAreUniform(t *testing.T) {
	dir := t.TempDir()
	f := nativeHistory(t, dir)
	sender := historyIdentity(t, f, "sender")
	recipient := historyIdentity(t, f, "recipient")
	for n := 0; n < 3; n++ {
		historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "cursor evidence"})
	}
	settledHistory(t, f, sender, false)
	first, err := any(f.eng).(historyAPI).ReadMailHistory(f.ctx, sender, 0, 1, false, "")
	if err != nil {
		t.Fatal("setup: initial page:", err)
	}
	cursor, ok := first["cursor"].(string)
	if !ok || cursor == "" {
		t.Fatal("setup: server issued no continuation:", first)
	}
	for _, value := range []string{"garbage", forgedHistoryCursor(t, cursor)} {
		assertInvalidReviewCursor(t, f, sender, value, "forged or foreign cursor confirmed a position")
	}
	assertInvalidReviewCursor(t, f, recipient, cursor, "forged or foreign cursor confirmed a position")
	reader := core.Agent{ID: f.ids[sender], CreatedSerial: 999999}
	status := f.led.MailHistory().Status()
	_, err = f.led.MailHistory().ReadPage(f.ctx, mailhistory.PageRequest{Reader: reader, Upper: status.Drained.Serial, OwnershipChange: status.OwnershipChange, Limit: 1, Cursor: cursor, Deadline: time.Now().Add(time.Second)})
	if !errors.Is(err, mailhistory.ErrCursor) {
		t.Fatal("cursor replay to different incarnation succeeded", err)
	}
	f.stop()
	restart := nativeHistory(t, dir)
	restart.ids[sender] = f.ids[sender]
	settledHistory(t, restart, sender, false)
	resumed, err := any(restart.eng).(historyAPI).ReadMailHistory(restart.ctx, sender, 0, 1, false, cursor)
	if err != nil || len(historyUnits(t, resumed)) != 1 {
		t.Fatal("valid same-generation cursor did not survive restart:", err, resumed)
	}
}

func TestMailHistoryNativeInheritedReferenceForgeryRefusesLikeGarbage(t *testing.T) {
	dir := t.TempDir()
	historyBudgetLedger(t, dir, "history-native")
	f := nativeHistoryWithLimits(t, dir, core.DefaultLimits())
	f.ids["budget-token-heir"] = "heir"
	settledHistory(t, f, "budget-token-heir", false)
	first, err := any(f.eng).(historyAPI).ReadMailHistory(f.ctx, "budget-token-heir", 0, 1, false, "")
	if err != nil {
		t.Fatal("setup: actual inherited page:", err)
	}
	cursor, ok := first["cursor"].(string)
	if !ok || cursor == "" {
		t.Fatal("setup: inherited continuation:", first)
	}
	for _, value := range []string{"garbage", forgedHistoryCursor(t, cursor)} {
		assertInvalidReviewCursor(t, f, "budget-token-heir", value, "forged inherited reference confirmed another position")
	}
}

func TestMailHistoryNativeOversizedContentKeepsMetadataAndLaterRows(t *testing.T) {
	dir := t.TempDir()
	limits := core.DefaultLimits()
	limits.MaxBodyBytes = 18 << 20
	l, w := reviewLedger(t, dir, limits)
	w.apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "recipient", MsgType: core.MsgNotify, Body: strings.Repeat("x", 17<<20)})
	w.apply(&core.Op{Kind: core.OpSendMessage, Token: "sender-token", To: "recipient", MsgType: core.MsgNotify, Body: "LATER-SMALL-CONTENT"})
	if err := l.Close(); err != nil {
		t.Fatal("setup:", err)
	}
	f := nativeHistoryWithLimits(t, dir, limits)
	f.ids["sender-token"] = "sender"
	settledHistory(t, f, "sender-token", false)
	res, err := any(f.eng).(historyAPI).ReadMailHistory(f.ctx, "sender-token", 0, 100, true, "")
	raw := historyJSON(t, res)
	if err != nil || !strings.Contains(raw, "native content work bound") || !strings.Contains(raw, "LATER-SMALL-CONTENT") || len(historyUnits(t, res)) != 2 {
		t.Fatal("oversized content poisoned metadata or later rows:", err, raw)
	}
}

type expiredFirstContent struct{ *ledger.Ledger }

func (s *expiredFirstContent) ReadHistoryOp(ctx context.Context, serial uint64, seek mailhistory.SeekRange) (*core.Op, error) {
	// Validate the actual interval before its real query deadline expires.
	_, err := s.Ledger.ReadHistoryOp(ctx, serial, seek)
	if err != nil {
		return nil, err
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestMailHistoryNativeFirstContentDeadlineKeepsMetadata(t *testing.T) {
	f := nativeHistory(t, t.TempDir(), func(l *ledger.Ledger) engine.Ledger { return &expiredFirstContent{l} })
	sender := historyIdentity(t, f, "sender")
	historyIdentity(t, f, "recipient")
	historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "native deadline evidence"})
	settledHistory(t, f, sender, false)
	res, err := any(f.eng).(historyAPI).ReadMailHistory(f.ctx, sender, 0, 25, true, "")
	if err != nil || len(historyUnits(t, res)) != 1 || !strings.Contains(historyJSON(t, res), "native content work bound") {
		t.Fatal("first content deadline poisoned authorized metadata:", err, res)
	}
}

func TestMailHistoryNativeAuthorMachineIsEventTimeEvidence(t *testing.T) {
	f := nativeHistory(t, t.TempDir())
	sender := historyIdentity(t, f, "sender")
	historyIdentity(t, f, "recipient")
	historyOp(t, f, &core.Op{Kind: core.OpUpdate, Token: sender, Agent: &core.AgentInfo{HostID: "machine-before"}})
	historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "before identity update"})
	historyOp(t, f, &core.Op{Kind: core.OpUpdate, Token: sender, Name: "sender-renamed", Agent: &core.AgentInfo{HostID: "machine-after"}})
	historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "after identity update"})
	rows := historyUnits(t, settledHistory(t, f, sender, false))
	if len(rows) != 2 {
		t.Fatal("setup: authored unit count", rows)
	}
	first, ok := rows[0]["unit"].(mailhistory.Unit)
	if !ok {
		t.Fatal("setup: typed projected unit", rows[0])
	}
	second := rows[1]["unit"].(mailhistory.Unit)
	if first.Author.Host != "machine-before" || second.Author.Host != "machine-after" || first.Author.ID != second.Author.ID {
		t.Fatal("identity update rewrote event-time attribution", first.Author, second.Author)
	}
}

func TestMailHistoryNativeSameKeyDifferentGenerationRefusesCursor(t *testing.T) {
	firstDir, secondDir := t.TempDir(), t.TempDir()
	first := nativeHistory(t, firstDir)
	sender := historyIdentity(t, first, "sender")
	historyIdentity(t, first, "recipient")
	for n := 0; n < 3; n++ {
		historyOp(t, first, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "first generation"})
	}
	settledHistory(t, first, sender, false)
	page, err := any(first.eng).(historyAPI).ReadMailHistory(first.ctx, sender, 0, 1, false, "")
	if err != nil {
		t.Fatal("setup: issued generation cursor:", err)
	}
	cursor, ok := page["cursor"].(string)
	if !ok || cursor == "" {
		t.Fatal("setup: generation cursor:", page)
	}
	key, err := os.ReadFile(filepath.Join(firstDir, "key"))
	if err != nil {
		t.Fatal("setup: existing board key:", err)
	}
	if err := os.WriteFile(filepath.Join(secondDir, "key"), key, 0o600); err != nil {
		t.Fatal("setup: second native board key:", err)
	}
	clear(key)
	second := nativeHistory(t, secondDir)
	secondSender := historyIdentity(t, second, "sender")
	historyIdentity(t, second, "recipient")
	for n := 0; n < 3; n++ {
		historyOp(t, second, &core.Op{Kind: core.OpSendMessage, Token: secondSender, To: "recipient", MsgType: core.MsgNotify, Body: "different generation"})
	}
	settledHistory(t, second, secondSender, false)
	if first.led.MailHistory().Status().Generation == second.led.MailHistory().Status().Generation {
		t.Fatal("setup: distinct native ledgers have same generation")
	}
	assertInvalidReviewCursor(t, second, secondSender, cursor, "different ledger generation cursor disclosed a row")
}
