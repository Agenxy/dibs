package engine

import (
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
)

// A notice that says "read_mail(N)" is cleared by reading N.
//
// Found by dogfooding. `ring-demo APPROVED your request (msg 712). Read it with
// read_mail to see what they said` arrived, read_mail(712) returned the message
// terminal and consumed, and the wake path delivered the same notice again on
// the next turn, because only check_in cleared notices. Following an
// instruction has to satisfy it: a channel that repeats itself after you obey
// it teaches agents to stop reading it, and this is the channel that has to
// stay worth reading.
func TestReadingAMessageClearsTheNoticePointingAtIt(t *testing.T) {
	e := &Engine{notices: map[string][]notice{}}

	// Through noteEvent, NOT by constructing a notice by hand. The first
	// version of this test built one with the message serial in the Serial
	// field, which is not what the real path does: it pushes the EVENT serial
	// and carries the message separately. The test passed, the product did not,
	// and the notice went on repeating after being obeyed. A fixture that
	// asserts your assumption tests your assumption.
	e.noteEvent(core.Event{
		Type: "message.approved", Agent: "approver", To: "reader", Serial: 749,
		Data: map[string]any{"msg_serial": uint64(746)},
	})
	e.noteEvent(core.Event{
		Type: "message.answered", Agent: "other", To: "reader", Serial: 810,
		Data: map[string]any{"msg_serial": uint64(800)},
	})
	if got := len(e.pendingNotices("reader")); got != 2 {
		t.Fatalf("setup: %d notices, want 2", got)
	}

	// Reading 746 satisfies the notice that said to read 746.
	e.clearNoticesFor("reader", 746)

	left := e.pendingNotices("reader")
	if len(left) != 1 {
		t.Fatalf("after reading 746, %d notices remain, want 1: %v", len(left), left)
	}
	// The unrelated one survives: clearing everything would swap a nagging
	// channel for a lossy one, which is the worse failure.
	if !strings.Contains(left[0], "800") {
		t.Errorf("the wrong notice survived: %q", left[0])
	}
}

// Reading a message clears its notice through the real call path, not just the
// helper: the helper being right is not what broke, the wiring is.
func TestGetMessageClearsTheNotice(t *testing.T) {
	e, ctx, cancel := runningEngine(t)
	defer cancel()

	reg := func(name, nonce string) string {
		t.Helper()
		r, err := e.Do(ctx, &core.Op{
			Kind: core.OpRegister, Name: name, AgentKind: core.KindPersistent, Nonce: nonce,
		})
		if err != nil {
			t.Fatal("setup:", err)
		}
		tok, _ := r["token"].(string)
		if tok == "" {
			t.Fatal("setup: no token")
		}
		return tok
	}
	readerTok := reg("reader", "n-r")
	senderTok := reg("sender", "n-s")

	sent, err := e.Do(ctx, &core.Op{
		Kind: core.OpSendMessage, Token: senderTok, To: "reader",
		MsgType: core.MsgQuestion, Body: "anything at all",
	})
	if err != nil {
		t.Fatal("setup:", err)
	}
	serial, _ := sent["msg_serial"].(uint64)
	if serial == 0 {
		t.Fatalf("setup: no serial in %v", sent)
	}

	// A notice pointing at that message, in the shape the real event path
	// produces: the EVENT serial is its own, and the message it points at is
	// carried separately. Getting that wrong is what made the first version of
	// this fix clear nothing.
	e.pushNoticeFor("reader", "sender answered your question", serial+9, serial)

	if _, err := e.GetMessage(ctx, readerTok, serial); err != nil {
		t.Fatal(err)
	}
	if left := e.pendingNotices("reader"); len(left) != 0 {
		t.Errorf("read_mail left the notice that told it to read: %v. The wake path "+
			"will repeat this every turn for a message already read", left)
	}
}
