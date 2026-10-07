package mailhistory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mailhistory"
)

// A real native content read completes first; only its return to the query is
// held. This tests the consumer port's runtime race without manufacturing an
// authorization flag, a decrypted op or an index unit.
type heldHistoryContent struct {
	*ledger.Ledger
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (h *heldHistoryContent) ReadHistoryOp(ctx context.Context, serial uint64, seek mailhistory.SeekRange) (*core.Op, error) {
	op, err := h.Ledger.ReadHistoryOp(ctx, serial, seek)
	if err != nil {
		return nil, err
	}
	h.once.Do(func() { close(h.entered); <-h.resume })
	return op, nil
}

func TestMailHistoryNativeOwnershipChangeAfterContentReadRefusesWholePage(t *testing.T) {
	var source *heldHistoryContent
	f := nativeHistory(t, t.TempDir(), func(led *ledger.Ledger) engine.Ledger {
		source = &heldHistoryContent{Ledger: led, entered: make(chan struct{}), resume: make(chan struct{})}
		return source
	})
	sender := historyIdentity(t, f, "sender")
	old := historyIdentity(t, f, "old")
	heir := historyIdentity(t, f, "heir")
	admin := historyIdentity(t, f, "admin")
	historyOp(t, f, &core.Op{Kind: core.OpGrantRole, To: "admin", Mode: core.RoleAdmin})
	parent := historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "old", MsgType: core.MsgNotify, Body: "PRIVATE-READ-BEFORE-MOVE"})["msg_serial"].(uint64)
	settledHistory(t, f, old, false)
	var once sync.Once
	resume := func() { once.Do(func() { close(source.resume) }) }
	defer resume()
	type reply struct {
		res core.Result
		err error
	}
	completed := make(chan reply, 1)
	api, ok := any(f.eng).(historyAPI)
	if !ok {
		t.Fatal("history public query missing after real setup")
	}
	go func() { res, err := api.ReadMailHistory(f.ctx, old, 0, 100, true, ""); completed <- reply{res, err} }()
	select {
	case <-source.entered:
	case <-f.ctx.Done():
		t.Fatal("setup: actual native content read never completed")
	}
	historyOp(t, f, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"old"}})
	moved := historyOp(t, f, &core.Op{Kind: core.OpAdoptAgent, Token: admin, To: "old", Space: "heir"})
	if moved["messages"] != 1 {
		t.Fatal("setup: actual adoption did not move mail", moved)
	}
	historyOp(t, f, &core.Op{Kind: core.OpAckMessage, Token: heir, MsgSerial: parent})
	historyOp(t, f, &core.Op{Kind: core.OpSweep, PurgeMail: true})
	resume()
	select {
	case r := <-completed:
		ce, ok := r.res["error"].(*core.Error)
		var returned *core.Error
		if !errors.As(r.err, &returned) || !ok || ce.Code != "E_HISTORY_SETTLING" || returned.Code != ce.Code {
			t.Fatal("ownership move during actual I/O must refuse whole page:", r)
		}
		if r.res["units"] != nil || strings.Contains(historyJSON(t, r.res), "PRIVATE-READ-BEFORE-MOVE") {
			t.Fatal("final reauth leaked a row:", r.res)
		}
	case <-f.ctx.Done():
		t.Fatal("history return hung after actual content read")
	}
	if raw := historyJSON(t, settledHistory(t, f, heir, true)); !strings.Contains(raw, "PRIVATE-READ-BEFORE-MOVE") {
		t.Fatal("new owner cannot observe committed evidence:", raw)
	}
}

func TestMailHistoryNativeQuotedAttachmentsAndCredentialExclusion(t *testing.T) {
	f := nativeHistory(t, t.TempDir())
	sender := historyIdentity(t, f, "sender")
	recipient := historyIdentity(t, f, "recipient")
	path := "/this/path/does/not/exist/PRIVATE-ATTACHMENT"
	historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "PRIVATE-BODY", Attachments: []core.Attachment{{Path: path, Size: 17, Hash: "claimed hash"}}, Agent: &core.AgentInfo{HostID: "recorded-host"}})
	res := settledHistory(t, f, recipient, true)
	raw := historyJSON(t, res)
	if !strings.Contains(raw, path) || !strings.Contains(raw, `"recorded_count":1`) || strings.Contains(raw, sender) || strings.Contains(raw, recipient) {
		t.Fatal("quoted fileref lost or credential appeared:", raw)
	}
	// Round-tripping the response must not require a live local path or blob.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["units"] == nil {
		t.Fatal("missing quoted-data response")
	}
}

func TestMailHistoryNativeBusyWriterNeverRegressesToWarming(t *testing.T) {
	f := nativeHistory(t, t.TempDir())
	sender := historyIdentity(t, f, "sender")
	_ = historyIdentity(t, f, "recipient")
	settledHistory(t, f, sender, false)
	api, ok := any(f.eng).(historyAPI)
	if !ok {
		t.Fatal("history public query missing")
	}
	stopped := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stopped:
				finished <- nil
				return
			default:
			}
			if err := f.eng.SetRateTokens(f.ctx, f.ids[sender], 30); err != nil {
				finished <- err
				return
			}
			res, err := f.eng.Do(f.ctx, &core.Op{Kind: core.OpAckBoard, Token: sender})
			if err != nil {
				finished <- err
				return
			}
			if res["error"] != nil {
				finished <- res["error"].(*core.Error)
				return
			}
		}
	}()
	var once sync.Once
	stop := func() { once.Do(func() { close(stopped) }) }
	defer stop()
	for n := 0; n < 10; n++ {
		if err := f.eng.SetRateTokens(f.ctx, f.ids[sender], 30); err != nil {
			t.Fatal(err)
		}
		res, err := api.ReadMailHistory(f.ctx, sender, 0, 25, false, "")
		if err != nil || res["error"] != nil || res["as_of_serial"] == nil || res["behind_by"] == nil {
			t.Fatal("ordinary writes caused a WARMING flap or lost lag:", err, res)
		}
	}
	stop()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal("setup: busy writer:", err)
		}
	case <-time.NewTimer(time.Second).C:
		t.Fatal("busy writer did not stop")
	}
}

func TestMailHistoryNativeTokenRevokedAfterContentReadRefusesWholePage(t *testing.T) {
	var source *heldHistoryContent
	f := nativeHistory(t, t.TempDir(), func(led *ledger.Ledger) engine.Ledger {
		source = &heldHistoryContent{Ledger: led, entered: make(chan struct{}), resume: make(chan struct{})}
		return source
	})
	sender := historyIdentity(t, f, "sender")
	recipient := historyIdentity(t, f, "recipient")
	historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "PRIVATE-REVOKED-TOKEN"})
	settledHistory(t, f, recipient, false)
	var once sync.Once
	resume := func() { once.Do(func() { close(source.resume) }) }
	defer resume()
	completed := make(chan core.Result, 1)
	api, ok := any(f.eng).(historyAPI)
	if !ok {
		t.Fatal("history public query missing after real setup")
	}
	go func() {
		res, err := api.ReadMailHistory(f.ctx, recipient, 0, 100, true, "")
		if err != nil {
			var ce *core.Error
			if errors.As(err, &ce) {
				res = core.Result{"error": ce}
			} else {
				res = core.Result{"unexpected_transport_error": err.Error()}
			}
		}
		completed <- res
	}()
	select {
	case <-source.entered:
	case <-f.ctx.Done():
		t.Fatal("setup: actual content read never completed")
	}
	historyOp(t, f, &core.Op{Kind: core.OpPrune, To: "recipient"})
	// The real closed row no longer authenticates; no test mutation of tokens.
	if _, _, err := f.eng.SubscribeInfo(f.ctx, recipient); err == nil {
		t.Fatal("setup: actual prune did not revoke token")
	}
	resume()
	select {
	case res := <-completed:
		ce, ok := res["error"].(*core.Error)
		if !ok || ce.Code != core.ErrBadToken.Code || res["units"] != nil {
			t.Fatal("final token check disclosed a page:", res)
		}
	case <-f.ctx.Done():
		t.Fatal("query hung after actual token revocation")
	}
}

func TestMailHistoryNativeContentAuthenticatesTheSuffixAfterRequestedRecord(t *testing.T) {
	dir := t.TempDir()
	f := nativeHistory(t, dir)
	sender := historyIdentity(t, f, "sender")
	_ = historyIdentity(t, f, "recipient")
	historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "PRIVATE-AUTHENTICATED-SUFFIX"})
	historyOp(t, f, &core.Op{Kind: core.OpAckBoard, Token: sender})
	settledHistory(t, f, sender, false)
	// Alter only a later complete record with valid JSON and identical length.
	// Its previous link still matches, so checking only the chain before the
	// requested content is insufficient. The committed drained hash must fail.
	path := filepath.Join(dir, "ledger.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("setup:", err)
	}
	start := bytes.LastIndex(raw[:len(raw)-1], []byte("\n")) + 1
	tail := raw[start:]
	altered := bytes.Replace(tail, []byte("check_in"), []byte("check_io"), 1)
	if bytes.Equal(tail, altered) {
		t.Fatal("setup: final real checkpoint was not found")
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal("setup:", err)
	}
	n, writeErr := file.WriteAt(altered, int64(start))
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || n != len(altered) {
		t.Fatal("setup: corruption was not persisted:", n, writeErr, syncErr, closeErr)
	}
	res := historyOnce(t, f, sender, true)
	ce, ok := res["error"].(*core.Error)
	if !ok || ce.Code != "E_HISTORY_UNAVAILABLE" || res["units"] != nil || strings.Contains(historyJSON(t, res), "PRIVATE-AUTHENTICATED-SUFFIX") {
		t.Fatal("unauthenticated native suffix disclosed content:", res)
	}
	if raw := historyJSON(t, historyOnce(t, f, sender, false)); strings.Contains(raw, "PRIVATE-AUTHENTICATED-SUFFIX") {
		t.Fatal("metadata disclosed authored content:", raw)
	}
}

func TestMailHistoryNativeReusedNameDoesNotInheritPredecessorEvidence(t *testing.T) {
	limits := core.DefaultLimits()
	limits.ConsumedRetention = 0
	limits.DormancyMax = 0
	limits.ArchiveRetention = 0
	f := nativeHistoryWithLimits(t, t.TempDir(), limits)
	sender := historyIdentity(t, f, "sender")
	previous := historyIdentity(t, f, "recipient")
	historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "PRIVATE-PREDECESSOR"})
	settledHistory(t, f, previous, false)
	// Real dormancy, archive and agent GC, preserving historical mail through
	// the sweep's explicit purge_mail=false. A closed row reserves its id and
	// cannot test reuse; a genuinely removed row can.
	historyOp(t, f, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"recipient"}})
	historyOp(t, f, &core.Op{Kind: core.OpSweep})
	historyOp(t, f, &core.Op{Kind: core.OpSweep})
	res := historyOp(t, f, &core.Op{Kind: core.OpRegister, Name: "recipient", Nonce: "different-incarnation-nonce", PID: 1, AgentKind: core.KindPersistent})
	current, ok := res["token"].(string)
	if !ok || current == "" || current == previous || res["agent_id"] != f.ids[previous] {
		t.Fatal("setup: same immutable spelling was not reused:", res)
	}
	f.ids[current] = res["agent_id"].(string)
	raw := historyJSON(t, settledHistory(t, f, current, true))
	if strings.Contains(raw, "PRIVATE-PREDECESSOR") || !strings.Contains(raw, `"units":[]`) {
		t.Fatal("new occupant inherited predecessor history:", raw)
	}
}

func TestMailHistoryNativeObservationDoesNotWakeOrAdvanceDurableState(t *testing.T) {
	dir := t.TempDir()
	f := nativeHistory(t, dir)
	sender := historyIdentity(t, f, "sender")
	recipient := historyIdentity(t, f, "recipient")
	parent := historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "PRIVATE-OBSERVATION"})["msg_serial"].(uint64)
	historyOp(t, f, &core.Op{Kind: core.OpSweep, DeadAgents: []string{"recipient"}})
	_, before, err := f.eng.SubscribeInfo(f.ctx, "")
	if err != nil {
		t.Fatal("setup:", err)
	}
	raw := historyJSON(t, settledHistory(t, f, recipient, true))
	if !strings.Contains(raw, "PRIVATE-OBSERVATION") {
		t.Fatal("observation did not read dormant party's evidence:", raw)
	}
	f.stop()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal("setup:", err)
	}
	led, err := ledger.OpenReadOnly(filepath.Join(dir, "ledger.jsonl"), "history-native", box)
	if err != nil {
		t.Fatal("setup:", err)
	}
	defer func() { _ = led.Close() }()
	limits := core.DefaultLimits()
	limits.ConsumedRetention = 0
	st := core.NewState("history-native", limits)
	if _, err := led.Replay(st); err != nil {
		t.Fatal("setup:", err)
	}
	actor := st.Agents[f.ids[recipient]]
	if st.Serial != before || actor == nil || actor.Status != core.StatusDormant || st.Messages[parent] == nil || st.Messages[parent].DeliveredAt != 0 {
		t.Fatalf("history observation woke a sleeping row or advanced durable/read state: before=%d after=%d actor=%+v message=%+v", before, st.Serial, actor, st.Messages[parent])
	}
}

// Log the actual native port outcome before the engine sanitizes it. This
// changes neither the interval, the context deadline nor the reader's result.
type observedHistorySeek struct {
	*ledger.Ledger
	t *testing.T
}

func (s *observedHistorySeek) ReadHistoryOp(ctx context.Context, serial uint64, seek mailhistory.SeekRange) (*core.Op, error) {
	started := time.Now()
	op, err := s.Ledger.ReadHistoryOp(ctx, serial, seek)
	s.t.Logf("HISTORY_NATIVE_SEEK serial=%d bytes=%d elapsed=%s error=%v", serial, seek.End-seek.Start.Offset, time.Since(started), err)
	return op, err
}

func TestMailHistoryNativeSmallBodySurvivesLargeValidSeekInterval(t *testing.T) {
	dir := t.TempDir()
	wrap := func(led *ledger.Ledger) engine.Ledger { return &observedHistorySeek{Ledger: led, t: t} }
	f := nativeHistory(t, dir, wrap)
	sender := historyIdentity(t, f, "sender")
	recipient := historyIdentity(t, f, "recipient")
	first := historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: "SMALL-FIRST-QUOTED"})["msg_serial"].(uint64)
	historyOp(t, f, &core.Op{Kind: core.OpAckMessage, Token: recipient, MsgSerial: first})
	for n := 0; n < 400; n++ {
		parent := historyOp(t, f, &core.Op{Kind: core.OpSendMessage, Token: sender, To: "recipient", MsgType: core.MsgNotify, Body: strings.Repeat("b", 32<<10)})["msg_serial"].(uint64)
		historyOp(t, f, &core.Op{Kind: core.OpAckMessage, Token: recipient, MsgSerial: parent})
		if n%32 == 31 {
			historyOp(t, f, &core.Op{Kind: core.OpSweep, PurgeMail: true})
		}
	}
	info, err := os.Stat(filepath.Join(dir, "ledger.jsonl"))
	if err != nil || info.Size() <= 16<<20 {
		t.Fatal("setup: native interval is not wider than the content bound:", info, err)
	}
	readSmall := func(f nativeHistoryFixture) {
		t.Helper()
		api, ok := any(f.eng).(historyAPI)
		if !ok {
			t.Fatal("history public query missing after real setup")
		}
		// Settle metadata through the same API before asking for native content;
		// the guard discriminates span sizing, not initial bootstrap readiness.
		settledHistory(t, f, sender, false)
		res, err := api.ReadMailHistory(f.ctx, sender, 0, 1, true, "")
		if err != nil || res["error"] != nil || !strings.Contains(historyJSON(t, res), "SMALL-FIRST-QUOTED") {
			t.Fatal("small authorized body remained unavailable in a large valid interval:", err, res)
		}
	}
	readSmall(f) // byte-bounded live capture
	f.stop()
	restarted := nativeHistory(t, dir, wrap)
	restarted.ids[sender] = f.ids[sender]
	readSmall(restarted) // canonical bootstrap independently rebuilds the anchors
}
