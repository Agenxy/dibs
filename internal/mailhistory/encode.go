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

type deltaEncoder struct {
	encoder
	mask uint32
	bit  uint
	full bool
}

func (e *deltaEncoder) changed(changed bool) bool {
	write := e.full || changed
	if write {
		e.mask |= uint32(1) << e.bit
	}
	e.bit++
	return write
}

func (e *deltaEncoder) number(n, previous uint64) {
	if e.changed(n != previous) {
		e.encoder.number(n)
	}
}

func (e *deltaEncoder) integer(n, previous int) {
	if e.changed(n != previous) {
		e.encoder.integer(n)
	}
}

func (e *deltaEncoder) text(s, previous string) {
	if e.changed(s != previous) {
		e.encoder.text(s)
	}
}

func (e *deltaEncoder) timestamp(t, previous time.Time) {
	if e.changed(t != previous) {
		e.encoder.timestamp(t)
	}
}

func encodeUnit(buf []byte, u snapshotUnit, previous *snapshotUnit) ([]byte, error) {
	e := deltaEncoder{encoder: encoder{buf: append(buf[:0], 2, 0, 0, 0, 0)}, full: previous == nil}
	if previous == nil {
		previous = &snapshotUnit{}
	}
	m, p := &u.Metadata, &previous.Metadata
	e.number(u.Position.Op, previous.Position.Op)
	e.number(u.Position.Msg, previous.Position.Msg)
	e.number(uint64(u.Position.Ord), uint64(previous.Position.Ord))
	e.timestamp(u.At, previous.At)
	e.text(u.Kind, previous.Kind)
	e.text(m.From, p.From)
	e.text(m.To, p.To)
	e.text(m.Type, p.Type)
	e.text(m.State, p.State)
	e.text(m.AdoptedFrom, p.AdoptedFrom)
	e.number(snapshotFlags(u), snapshotFlags(*previous))
	for _, pair := range [...][2]uint64{
		{m.AdoptedAt, p.AdoptedAt},
		{m.Delivered, p.Delivered},
		{m.Responded, p.Responded},
		{m.Acked, p.Acked},
		{m.OutcomeRead, p.OutcomeRead},
		{m.ReviewRead, p.ReviewRead},
		{m.ReviewCutoff, p.ReviewCutoff},
		{m.Superseded, p.Superseded},
		{m.QueueChanged, p.QueueChanged},
		{u.Author.Created, previous.Author.Created},
	} {
		e.number(pair[0], pair[1])
	}
	for _, pair := range [...][2]time.Time{
		{m.SentAt, p.SentAt},
		{m.DeliveredAt, p.DeliveredAt},
		{m.TerminalAt, p.TerminalAt},
		{m.RetainUntil, p.RetainUntil},
		{m.Deadline, p.Deadline},
	} {
		e.timestamp(pair[0], pair[1])
	}
	e.text(m.QueuePriority, p.QueuePriority)
	e.integer(m.QueueRank, p.QueueRank)
	e.integer(m.Milestones, p.Milestones)
	e.integer(m.Progress, p.Progress)
	e.text(u.Author.ID, previous.Author.ID)
	e.text(u.Author.Host, previous.Author.Host)
	if e.err == nil && e.bit != 32 {
		e.err = errors.New("invalid history field count")
	}
	if e.err == nil {
		binary.LittleEndian.PutUint32(e.buf[1:5], e.mask)
	}
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
