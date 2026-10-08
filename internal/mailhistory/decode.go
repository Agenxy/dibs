// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

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

func (d *decoder) unit() snapshotUnit {
	u := d.previous
	if len(d.buf) < 9 || d.buf[0] != 3 {
		d.err = errors.New("unknown derived history version")
		return u
	}
	mask := binary.LittleEndian.Uint64(d.buf[1:9])
	d.buf = d.buf[9:]
	if mask&^fullFields != 0 || (!d.known && mask != fullFields) {
		d.err = errors.New("first history unit is not self-contained")
		return u
	}
	fields := canonicalFields(&u)
	for bit, kind := range fieldKinds {
		if mask&(uint64(1)<<bit) == 0 {
			continue
		}
		d.field(kind, &fields[bit])
	}
	if fields[10].number >= 1<<6 {
		d.err = errors.New("invalid history flags")
	}
	u = restoreFields(fields)
	if d.err == nil {
		d.previous, d.known = u, true
	}
	return u
}

func (d *decoder) field(kind fieldKind, v *fieldValue) {
	switch kind {
	case wideField:
		v.number = d.number()
	case narrowField:
		v.number = d.number()
		if v.number > math.MaxUint32 {
			d.err = errors.New("invalid history narrow field")
		}
	case signedField:
		v.number = uint64(d.integer()) // #nosec G115 -- native signed bit pattern
	case textField:
		v.text = d.text()
	case timeField:
		v.at = d.timestamp()
	}
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
