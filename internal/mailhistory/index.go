package mailhistory

import (
	"sort"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Record describes an already persisted or replay-validated record, including
// the chain state needed for sparse content seeks. Serial gaps are supported.
type Record struct {
	Serial      uint64
	At          time.Time
	Offset, End int64
	Prev, Hash  [32]byte
}

// Anchor bounds a chain-validated seek in the existing encrypted ledger.
type Anchor struct {
	Serial uint64
	Offset int64
	Prev   [32]byte
}

// Index stores no bodies, token, response, participant path or author text.
// All state is derived from a single canonical fold; losing it loses no mail.
type Index struct {
	mu         sync.RWMutex
	codec      codec
	serials    vector
	latest     vector
	parties    map[partyKey]*party
	anchors    []Anchor
	records    uint64
	head       Record
	generation string
	ready      bool
	failed     bool
}

// New returns an empty derived index for a fresh ledger.
func New() *Index { return &Index{parties: map[partyKey]*party{}, ready: true} }

// Invalidate refuses history queries when a committed-record anchor is unusable.
// The derived view cannot turn a malformed anchor into a prior record's history.
func (i *Index) Invalidate() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.failed = true
}

// BeginReplay discards a derived view and refuses queries until replay finishes.
func (i *Index) BeginReplay() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.codec, i.serials, i.latest = codec{}, vector{}, vector{}
	i.parties = map[partyKey]*party{}
	i.anchors = nil
	i.records, i.head, i.generation = 0, Record{}, ""
	i.ready, i.failed = false, false
}

// EndReplay seals the tail only after the reader validates the whole prefix.
func (i *Index) EndReplay() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.codec.flush(); err != nil {
		i.failed = true
	}
	i.ready = true
}

func (s Snapshot) key(id string, st *core.State) partyKey {
	if a, ok := s.Authors[id]; ok {
		return partyKey{id, a.Created}
	}
	if a := st.Agents[id]; a != nil {
		return partyKey{id, a.CreatedSerial}
	}
	return partyKey{id, 0}
}

func (i *Index) party(key partyKey) *party {
	p := i.parties[key]
	if p == nil {
		p = &party{}
		i.parties[key] = p
	}
	return p
}

func (i *Index) add(u snapshotUnit, keys []partyKey) {
	ref := i.codec.units
	if err := i.codec.add(u); err != nil {
		i.failed = true
		return
	}
	n := sort.Search(i.serials.n, func(n int) bool { return i.serials.get(n) >= u.Position.Msg })
	switch {
	case n == i.serials.n:
		i.serials.add(u.Position.Msg)
		i.latest.add(ref)
	case i.serials.get(n) == u.Position.Msg:
		i.latest.set(n, ref)
	default:
		i.failed = true // no silently misplaced latest authorization header
		return
	}
	for _, key := range keys {
		i.party(key).refs.add(ref)
	}
}

// Measurement reports counts, not an estimated heap. Forced-GC paired runner
// measurements must include all vectors, prefix descriptors and codec buffers.
type Measurement struct {
	Units, Records                             uint64
	Blocks, Conversations, Parties, References int
	Prefixes, CompressedBytes, RawTailCapacity int
	Failed, Ready                              bool
}

// Measurement reports representation counts without estimating retained heap.
func (i *Index) Measurement() Measurement {
	i.mu.RLock()
	defer i.mu.RUnlock()
	m := Measurement{
		Units: i.codec.units, Records: i.records, Blocks: len(i.codec.blocks), Conversations: i.serials.n,
		Parties: len(i.parties), RawTailCapacity: cap(i.codec.tail), Failed: i.failed, Ready: i.ready,
	}
	for _, p := range i.parties {
		m.References += p.refs.n
		m.Prefixes += len(p.inherited)
	}
	for _, b := range i.codec.blocks {
		m.CompressedBytes += len(b.data)
	}
	return m
}
