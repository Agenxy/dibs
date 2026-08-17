package engine

import (
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

	e.pushNotice("reader", "somebody APPROVED your request (msg 712)", 712)
	e.pushNotice("reader", "somebody else answered (msg 800)", 800)

	e.clearNoticesFor("reader", 712)

	left := e.pendingNotices("reader")
	if len(left) != 1 {
		t.Fatalf("after reading 712, %d notices remain, want 1: %v", len(left), left)
	}
	// The OTHER one survives. Clearing everything would swap a nagging channel
	// for a lossy one, which is the worse failure of the two.
	if got := left[0]; got != "somebody else answered (msg 800)" {
		t.Errorf("the wrong notice survived: %q", got)
	}

	// And the unrelated one still clears on its own read.
	e.clearNoticesFor("reader", 800)
	if left := e.pendingNotices("reader"); len(left) != 0 {
		t.Errorf("notices outstanding after both were read: %v", left)
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

	// A notice pointing at that message, as answeredNotice would produce.
	e.pushNotice("reader", "sender answered your question", serial)

	if _, err := e.GetMessage(ctx, readerTok, serial); err != nil {
		t.Fatal(err)
	}
	if left := e.pendingNotices("reader"); len(left) != 0 {
		t.Errorf("read_mail left the notice that told it to read: %v. The wake path "+
			"will repeat this every turn for a message already read", left)
	}
}
