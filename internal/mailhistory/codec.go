package mailhistory

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"sort"
	"time"
)

const (
	blockUnits = 1024
	blockBytes = 128 << 10
)

type position struct {
	Op  uint64 `json:"op"`
	Msg uint64 `json:"msg"`
	Ord uint32 `json:"ord"`
}

type snapshotUnit struct {
	Position   position  `json:"position"`
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"`
	Metadata   Metadata  `json:"metadata"`
	Author     Author    `json:"author"`
	Content    bool      `json:"has_content,omitempty"`
	AfterKnown bool      `json:"after_known,omitempty"`
	Evicted    bool      `json:"evicted,omitempty"`
}

type block struct {
	first, lastOp uint64
	count         uint64
	raw           int
	data          []byte
}

// The raw live tail has a hard byte bound. No decoded unit or content is
// retained after sealing a block. The compressor is reused and charged.
type codec struct {
	blocks   []block
	tail     []byte
	scratch  []byte
	count    uint64
	units    uint64
	lastOp   uint64
	writer   *flate.Writer
	previous snapshotUnit
}

func (c *codec) add(u snapshotUnit) error {
	var previous *snapshotUnit
	if c.count > 0 {
		previous = &c.previous
	}
	raw, err := encodeUnit(c.scratch, u, previous)
	c.scratch = raw[:0]
	if err != nil || len(raw) > blockBytes {
		return errors.New("history snapshot exceeds its bounded block")
	}
	if c.count == blockUnits || len(c.tail)+len(raw) > blockBytes {
		if err := c.flush(); err != nil {
			return err
		}
		raw, err = encodeUnit(c.scratch, u, nil)
		c.scratch = raw[:0]
		if err != nil {
			return err
		}
	}
	if c.tail == nil {
		c.tail = make([]byte, 0, blockBytes)
	}
	c.tail = append(c.tail, raw...)
	c.count++
	c.units++
	c.lastOp = u.Position.Op
	c.previous = u
	return nil
}

func (c *codec) flush() error {
	if c.count == 0 {
		return nil
	}
	var out bytes.Buffer
	if c.writer == nil {
		w, err := flate.NewWriter(&out, flate.BestSpeed)
		if err != nil {
			return err
		}
		c.writer = w
	} else {
		c.writer.Reset(&out)
	}
	if _, err := c.writer.Write(c.tail); err != nil {
		return err
	}
	if err := c.writer.Close(); err != nil {
		return err
	}
	c.blocks = append(c.blocks, block{c.units - c.count, c.lastOp, c.count, len(c.tail), bytes.Clone(out.Bytes())})
	c.tail = c.tail[:0]
	c.count = 0
	c.previous = snapshotUnit{}
	return nil
}

func decode(data []byte, rawBytes int, count uint64) ([]snapshotUnit, error) {
	r := flate.NewReader(bytes.NewReader(data))
	defer func() { _ = r.Close() }()
	raw, err := io.ReadAll(io.LimitReader(r, blockBytes+1))
	if err != nil || len(raw) != rawBytes || len(raw) > blockBytes {
		return nil, errors.New("invalid bounded history block")
	}
	return decodeRaw(raw, count)
}

// Called under the index read lock. Page queries will copy bounded immutable
// block references and the raw tail, then decode outside the writer and lock.
func (c *codec) unit(ref uint64) (snapshotUnit, error) {
	n := sort.Search(len(c.blocks), func(n int) bool {
		return c.blocks[n].first+c.blocks[n].count > ref
	})
	var units []snapshotUnit
	var first uint64
	var err error
	if n < len(c.blocks) {
		b := c.blocks[n]
		first = b.first
		units, err = decode(b.data, b.raw, b.count)
	} else {
		first = c.units - c.count
		units, err = decodeRaw(c.tail, c.count)
	}
	if err != nil || ref < first || ref-first >= uint64(len(units)) {
		return snapshotUnit{}, errors.New("invalid history unit reference")
	}
	return units[ref-first], nil
}
