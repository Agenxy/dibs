package mailhistory

import (
	"encoding/binary"
	"errors"
	"math"
	"time"
)

type decoder struct {
	buf []byte
	err error
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

func (d *decoder) unit() snapshotUnit {
	var u snapshotUnit
	if d.number() != 1 {
		d.err = errors.New("unknown derived history version")
		return u
	}
	u.Position.Op, u.Position.Msg = d.number(), d.number()
	ordinal := d.number()
	if ordinal > math.MaxUint32 {
		d.err = errors.New("invalid history ordinal")
		return u
	}
	u.Position.Ord = uint32(ordinal)
	u.At, u.Kind = d.timestamp(), d.text()
	m := &u.Metadata
	m.From, m.To, m.Type, m.State, m.AdoptedFrom = d.text(), d.text(), d.text(), d.text(), d.text()
	flags := d.number()
	if flags >= 1<<6 {
		d.err = errors.New("invalid history flags")
	}
	m.Consumed, m.QueueDebt, m.QueueOrderLocked = flags&1 != 0, flags&2 != 0, flags&4 != 0
	u.Content, u.AfterKnown, u.Evicted = flags&8 != 0, flags&16 != 0, flags&32 != 0
	for _, n := range [...]*uint64{
		&m.AdoptedAt, &m.Delivered, &m.Responded, &m.Acked, &m.OutcomeRead, &m.ReviewRead,
		&m.ReviewCutoff, &m.Superseded, &m.QueueChanged, &u.Author.Created,
	} {
		*n = d.number()
	}
	for _, t := range [...]*time.Time{&m.SentAt, &m.DeliveredAt, &m.TerminalAt, &m.RetainUntil, &m.Deadline} {
		*t = d.timestamp()
	}
	m.QueuePriority = d.text()
	m.QueueRank, m.Milestones, m.Progress = d.integer(), d.integer(), d.integer()
	u.Author.ID, u.Author.Host = d.text(), d.text()
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
