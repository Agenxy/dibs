package mailhistory

import (
	"encoding/binary"
	"errors"
	"math"
	"time"
)

// This format is a disposable derived view, never a ledger field or cursor.
// Positional primitives avoid allocating JSON and formatting seven timestamps
// on the canonical writer for every unit. Every block is independently decoded.
type encoder struct {
	buf []byte
	err error
}

func (e *encoder) room(n int) bool {
	if e.err != nil {
		return false
	}
	if n < 0 || n > blockBytes-len(e.buf) {
		e.err = errors.New("history snapshot exceeds its bounded block")
		return false
	}
	return true
}

func (e *encoder) number(n uint64) {
	if e.room(binary.MaxVarintLen64) {
		e.buf = binary.AppendUvarint(e.buf, n)
	}
}

func (e *encoder) integer(n int) {
	if e.room(binary.MaxVarintLen64) {
		e.buf = binary.AppendVarint(e.buf, int64(n))
	}
}

func (e *encoder) text(s string) {
	e.number(uint64(len(s)))
	if e.room(len(s)) {
		e.buf = append(e.buf, s...)
	}
}

func (e *encoder) timestamp(t time.Time) {
	if t.IsZero() {
		e.number(0)
		return
	}
	if !e.room(32) {
		return
	}
	start := len(e.buf)
	e.buf = append(e.buf, 0)
	e.buf, e.err = t.AppendBinary(e.buf)
	if e.err == nil {
		size := len(e.buf) - start - 1
		if size < 0 || size > math.MaxUint8 {
			e.err = errors.New("invalid history timestamp length")
			return
		}
		e.buf[start] = byte(size)
	}
}

func encodeUnit(buf []byte, u snapshotUnit) ([]byte, error) {
	e := encoder{buf: buf[:0]}
	m := &u.Metadata
	e.number(1) // derived format version
	e.number(u.Position.Op)
	e.number(u.Position.Msg)
	e.number(uint64(u.Position.Ord))
	e.timestamp(u.At)
	e.text(u.Kind)
	e.text(m.From)
	e.text(m.To)
	e.text(m.Type)
	e.text(m.State)
	e.text(m.AdoptedFrom)
	e.number(snapshotFlags(u))
	for _, n := range [...]uint64{
		m.AdoptedAt, m.Delivered, m.Responded, m.Acked, m.OutcomeRead, m.ReviewRead,
		m.ReviewCutoff, m.Superseded, m.QueueChanged, u.Author.Created,
	} {
		e.number(n)
	}
	for _, t := range [...]time.Time{m.SentAt, m.DeliveredAt, m.TerminalAt, m.RetainUntil, m.Deadline} {
		e.timestamp(t)
	}
	e.text(m.QueuePriority)
	e.integer(m.QueueRank)
	e.integer(m.Milestones)
	e.integer(m.Progress)
	e.text(u.Author.ID)
	e.text(u.Author.Host)
	return e.buf, e.err
}

func snapshotFlags(u snapshotUnit) uint64 {
	var n uint64
	for bit, set := range [...]bool{
		u.Metadata.Consumed, u.Metadata.QueueDebt, u.Metadata.QueueOrderLocked, u.Content, u.AfterKnown, u.Evicted,
	} {
		if set {
			n |= 1 << bit
		}
	}
	return n
}
