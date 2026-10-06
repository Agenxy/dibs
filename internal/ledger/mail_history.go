package ledger

import (
	"encoding/hex"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/mailhistory"
)

// MailHistory exposes the derived committed-record view through the engine port.
func (l *Ledger) MailHistory() *mailhistory.Index { return l.mail }

// ObserveMail is called only for successful committed records. Replay enters
// this same observer after its one fold and chain/serial validation.
func (l *Ledger) ObserveMail(before mailhistory.Snapshot, st *core.State, op *core.Op, events []core.Event) {
	if l.mail != nil {
		l.mail.Observe(l.lastMail, before, st, op, events)
	}
}

func (l *Ledger) recordMail(serial uint64, at time.Time, offset, length int64, previous string, hash [32]byte) {
	l.lastMail = mailhistory.Record{}
	var prev [32]byte
	if previous != "" {
		decoded, err := hex.DecodeString(previous)
		if err != nil || len(decoded) != len(prev) {
			if l.mail != nil {
				l.mail.Invalidate()
			}
			return // normal chain validation still owns any ledger error
		}
		copy(prev[:], decoded)
	}
	l.lastMail = mailhistory.Record{Serial: serial, At: at, Offset: offset, End: offset + length, Prev: prev, Hash: hash}
}
