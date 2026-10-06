package mailhistory

import (
	"encoding/binary"
	"errors"
	"math"
	"time"
)

type decoder struct {
	buf      []byte
	err      error
	mask     uint32
	bit      uint
	previous snapshotUnit
	known    bool
}

func (d *decoder) number() uint64 {
	if d.err != nil {
		return 0
	}
	n, size := binary.Uvarint(d.buf)
	if size <= 0 {
		d.err = errors.New("invalid history number")
		return 0
	}
	d.buf = d.buf[size:]
	return n
}

func (d *decoder) integer() int {
	if d.err != nil {
		return 0
	}
	n, size := binary.Varint(d.buf)
	if size <= 0 || n < math.MinInt || n > math.MaxInt {
		d.err = errors.New("invalid history integer")
		return 0
	}
	d.buf = d.buf[size:]
	return int(n)
}

func (d *decoder) bytes() []byte {
	n := d.number()
	if d.err != nil || n > uint64(len(d.buf)) {
		d.err = errors.New("invalid bounded history value")
		return nil
	}
	v := d.buf[:n]
	d.buf = d.buf[n:]
	return v
}

func (d *decoder) text() string { return string(d.bytes()) }

func (d *decoder) timestamp() time.Time {
	v := d.bytes()
	var t time.Time
	if len(v) > 0 {
		if err := t.UnmarshalBinary(v); err != nil {
			d.err = err
		}
	}
	return t
}

func (d *decoder) changed() bool {
	set := d.mask&(uint32(1)<<d.bit) != 0
	d.bit++
	return set
}

func (d *decoder) deltaNumber(previous uint64) uint64 {
	if d.changed() {
		return d.number()
	}
	return previous
}

func (d *decoder) deltaInteger(previous int) int {
	if d.changed() {
		return d.integer()
	}
	return previous
}

func (d *decoder) deltaText(previous string) string {
	if d.changed() {
		return d.text()
	}
	return previous
}

func (d *decoder) deltaTimestamp(previous time.Time) time.Time {
	if d.changed() {
		return d.timestamp()
	}
	return previous
}

func (d *decoder) unit() snapshotUnit {
	u := d.previous
	if len(d.buf) < 5 || d.buf[0] != 2 {
		d.err = errors.New("unknown derived history version")
		return u
	}
	d.mask, d.bit = binary.LittleEndian.Uint32(d.buf[1:5]), 0
	d.buf = d.buf[5:]
	if !d.known && d.mask != ^uint32(0) {
		d.err = errors.New("first history unit is not self-contained")
		return u
	}
	u.Position.Op = d.deltaNumber(u.Position.Op)
	u.Position.Msg = d.deltaNumber(u.Position.Msg)
	ordinal := d.deltaNumber(uint64(u.Position.Ord))
	if ordinal > math.MaxUint32 {
		d.err = errors.New("invalid history ordinal")
		return u
	}
	u.Position.Ord = uint32(ordinal)
	u.At, u.Kind = d.deltaTimestamp(u.At), d.deltaText(u.Kind)
	m := &u.Metadata
	m.From, m.To, m.Type = d.deltaText(m.From), d.deltaText(m.To), d.deltaText(m.Type)
	m.State, m.AdoptedFrom = d.deltaText(m.State), d.deltaText(m.AdoptedFrom)
	flags := d.deltaNumber(snapshotFlags(u))
	if flags >= 1<<6 {
		d.err = errors.New("invalid history flags")
	}
	m.Consumed, m.QueueDebt, m.QueueOrderLocked = flags&1 != 0, flags&2 != 0, flags&4 != 0
	u.Content, u.AfterKnown, u.Evicted = flags&8 != 0, flags&16 != 0, flags&32 != 0
	for _, n := range [...]*uint64{
		&m.AdoptedAt, &m.Delivered, &m.Responded, &m.Acked, &m.OutcomeRead, &m.ReviewRead,
		&m.ReviewCutoff, &m.Superseded, &m.QueueChanged, &u.Author.Created,
	} {
		*n = d.deltaNumber(*n)
	}
	for _, t := range [...]*time.Time{&m.SentAt, &m.DeliveredAt, &m.TerminalAt, &m.RetainUntil, &m.Deadline} {
		*t = d.deltaTimestamp(*t)
	}
	m.QueuePriority = d.deltaText(m.QueuePriority)
	m.QueueRank = d.deltaInteger(m.QueueRank)
	m.Milestones, m.Progress = d.deltaInteger(m.Milestones), d.deltaInteger(m.Progress)
	u.Author.ID, u.Author.Host = d.deltaText(u.Author.ID), d.deltaText(u.Author.Host)
	if d.bit != 32 {
		d.err = errors.New("invalid history field count")
	}
	if d.err == nil {
		d.previous, d.known = u, true
	}
	return u
}

func decodeRaw(raw []byte, count uint64) ([]snapshotUnit, error) {
	if len(raw) > blockBytes || count > blockUnits {
		return nil, errors.New("invalid bounded history block")
	}
	d := decoder{buf: raw}
	units := make([]snapshotUnit, 0, count)
	for range count {
		units = append(units, d.unit())
		if d.err != nil {
			return nil, d.err
		}
	}
	if len(d.buf) != 0 {
		return nil, errors.New("trailing history snapshot bytes")
	}
	return units, nil
}
