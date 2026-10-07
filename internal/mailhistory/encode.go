package mailhistory

import (
	"encoding/binary"
	"errors"
	"math"
	"time"
)

// This format is a disposable derived view, never a ledger field or cursor.
// Serialization happens on the background builder, never canonical replay.
// Every block is independently decoded.
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

func encodeUnit(buf []byte, u snapshotUnit, previous *snapshotUnit) ([]byte, error) {
	mask := changedFields(&u, previous)
	e := encoder{buf: append(buf[:0], 3, 0, 0, 0, 0, 0, 0, 0, 0)}
	binary.LittleEndian.PutUint64(e.buf[1:9], mask)
	for bit, value := range canonicalFields(&u) {
		if mask&(uint64(1)<<bit) == 0 {
			continue
		}
		switch fieldKinds[bit] {
		case wideField, narrowField:
			e.number(value.number)
		case signedField:
			e.integer(int(value.number)) // #nosec G115 -- native signed bit pattern
		case textField:
			e.text(value.text)
		case timeField:
			e.timestamp(value.at)
		}
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
