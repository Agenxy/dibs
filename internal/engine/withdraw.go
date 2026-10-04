package engine

import (
	"fmt"
	"sort"

	"github.com/agenxy/dibs/internal/core"
)

func withdrawalNotice(ev core.Event) string {
	kind := core.MsgRequest // historical events before question withdrawal
	if ev.Data["message_type"] == core.MsgQuestion {
		kind = core.MsgQuestion
	}
	text := fmt.Sprintf("%s withdrew %s %v; read_mail has the reason", ev.Agent, kind, ev.Data["msg_serial"])
	if n, _ := ev.Data["superseded_by"].(uint64); n != 0 {
		text += fmt.Sprintf(" (superseded by %s %d)", kind, n)
	}
	return text
}

// This receipt belongs to the RECIPIENT. Neither the sender's verdict-read
// watermark nor the event ring can replace the envelope's durable ack.
func (e *Engine) rebuildWithdrawalNotices() {
	var mail []*core.Message
	for _, m := range e.state.Messages {
		if m.State == core.MsgStateWithdrawn && !m.Consumed && e.withdrawalReceiptBelongs(m) {
			mail = append(mail, m)
		}
	}
	sort.Slice(mail, func(i, j int) bool { return mail[i].RespondedAt < mail[j].RespondedAt })
	for _, m := range mail {
		ev := core.Event{
			Agent: m.WithdrawnBy, To: m.To,
			Data: map[string]any{"msg_serial": m.Serial, "superseded_by": m.SupersededBy, "message_type": m.Type},
		}
		e.pushNoticeAs(m.To, withdrawalNotice(ev), m.RespondedAt, m.Serial, true, m.TerminalAt)
	}
}

func (e *Engine) withdrawalReceiptBelongs(m *core.Message) bool {
	if m == nil {
		return false
	}
	l := e.state.Agents[m.To]
	return !l.Retired() && (l.CreatedSerial == 0 || m.Serial >= l.CreatedSerial || e.state.AdoptedFor(m, l.ID))
}
