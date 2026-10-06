package mailhistory

import "time"

const scalarChunk = 256

// Fixed segments keep growth from doubling a million-record capture. Narrow
// references are local to a bounded 1024-unit chunk; global serials stay wide.
type rawColumn[T any] struct {
	chunks []*[scalarChunk]T
	n      uint32
}

func (c *rawColumn[T]) add(v T) {
	if c.n%scalarChunk == 0 {
		c.chunks = append(c.chunks, &[scalarChunk]T{})
	}
	c.chunks[c.n/scalarChunk][c.n%scalarChunk] = v
	c.n++
}

func (c *rawColumn[T]) get(n uint32) T { return c.chunks[n/scalarChunk][n%scalarChunk] }

type rawRow struct {
	mask         uint64
	wide, narrow uint32
}

type rawMove struct {
	before   uint32
	from, to partyKey
}

type rawChunk struct {
	rows        [blockUnits]rawRow
	count       uint32
	wide        rawColumn[uint64]
	narrow      rawColumn[uint32]
	strings     rawColumn[string]
	stringBytes uint64
	times       rawColumn[time.Time]
	stringIDs   map[string]uint32
	timeIDs     map[time.Time]uint32
	previous    snapshotUnit
	moves       []rawMove
	head        Record
	next        *rawChunk
}

func newRawChunk() *rawChunk {
	return &rawChunk{stringIDs: make(map[string]uint32), timeIDs: make(map[time.Time]uint32)}
}

func (c *rawChunk) add(u snapshotUnit) {
	var previous *snapshotUnit
	if c.count > 0 {
		previous = &c.previous
	}
	r := rawRow{mask: changedFields(&u, previous), wide: c.wide.n, narrow: c.narrow.n}
	for bit, v := range canonicalFields(&u) {
		if r.mask&(uint64(1)<<bit) == 0 {
			continue
		}
		switch fieldKinds[bit] {
		case wideField, signedField:
			c.wide.add(v.number)
		case narrowField:
			c.narrow.add(uint32(v.number)) // #nosec G115 -- native ordinal/flags are uint32-bounded
		case textField:
			id, ok := c.stringIDs[v.text]
			if !ok {
				id = c.strings.n
				c.stringIDs[v.text] = id
				c.strings.add(v.text)
				c.stringBytes += uint64(len(v.text)) + 64
			}
			c.narrow.add(id)
		case timeField:
			id, ok := c.timeIDs[v.at]
			if !ok {
				id = c.times.n
				c.timeIDs[v.at] = id
				c.times.add(v.at)
			}
			c.narrow.add(id)
		}
	}
	c.rows[c.count] = r
	c.count++
	c.previous = u
}

func (c *rawChunk) unit(n uint32, previous snapshotUnit) snapshotUnit {
	r := c.rows[n]
	fields := canonicalFields(&previous)
	for bit, kind := range fieldKinds {
		if r.mask&(uint64(1)<<bit) == 0 {
			continue
		}
		v := &fields[bit]
		switch kind {
		case wideField, signedField:
			v.number = c.wide.get(r.wide)
			r.wide++
		case narrowField:
			v.number = uint64(c.narrow.get(r.narrow))
			r.narrow++
		case textField:
			v.text = c.strings.get(c.narrow.get(r.narrow))
			r.narrow++
		case timeField:
			v.at = c.times.get(c.narrow.get(r.narrow))
			r.narrow++
		}
	}
	return restoreFields(fields)
}

func (c *rawChunk) seal() {
	c.stringIDs, c.timeIDs = nil, nil
	c.previous = snapshotUnit{}
}

// This is a conservative queue charge, NOT a claimed heap measurement. The
// hosted peak includes allocation slack, maps, backing strings and Go runtime.
func (c *rawChunk) charge() uint64 {
	n := uint64(blockUnits*16 + len(c.wide.chunks)*scalarChunk*8 + len(c.narrow.chunks)*scalarChunk*4 +
		len(c.strings.chunks)*scalarChunk*16 + len(c.times.chunks)*scalarChunk*24 + cap(c.moves)*56)
	return n + c.stringBytes + uint64(c.times.n)*64
}
