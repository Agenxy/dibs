package mailhistory

import (
	"context"
	"crypto/rand"
	"errors"
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
	mu                  sync.RWMutex
	viewMu              sync.RWMutex // builder/view only; compression never holds the capture lock
	codec               codec
	serials             vector
	latest              vector
	parties             map[partyKey]*party
	anchors             anchorVector
	numbers             []uint64 // bounded writer scratch for sorted canonical changes
	records             uint64
	head                Record
	generation          string
	cursorKey           [32]byte
	ready               bool
	initialReady        bool // monotonic after S0; live writes only clear ready
	failed              bool
	ended               bool
	started             bool
	signal              chan struct{}
	active              *rawChunk
	first               *rawChunk
	last                *rawChunk
	queued              uint64
	queueLimit          uint64
	captured            uint64
	built               uint64
	builtHead           Record
	bootstrap           func(context.Context, *Index) error
	bootSerial          uint64
	lastOwnershipChange uint64 // writer-derived authorization fence, never ledger state
	anchorStart         int64  // live capture offset; independent from the bootstrap projector
	anchorSeen          bool
}

// New returns an empty derived index for a fresh ledger.
func New(keys ...[32]byte) *Index {
	i := &Index{
		parties: map[partyKey]*party{}, ready: true, initialReady: true, ended: true,
		signal: make(chan struct{}, 1), queueLimit: liveQueueBytes,
	}
	if len(keys) > 0 {
		i.cursorKey = keys[0]
	} else {
		// Standalone derived views get a private key; ledger views supply their
		// domain-separated stable board key. crypto/rand.Read cannot return an error.
		_, _ = rand.Read(i.cursorKey[:])
	}
	return i
}

// Invalidate refuses history queries when a committed-record anchor is unusable.
// The derived view cannot turn a malformed anchor into a prior record's history.
func (i *Index) Invalidate() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.failCapture()
}

// BeginReplay discards a derived view and refuses queries until replay finishes.
func (i *Index) BeginReplay() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.started {
		i.failed = true // replay belongs before serving, never concurrently with a builder
		return
	}
	i.codec, i.serials, i.latest = codec{}, vector{}, vector{}
	i.parties = map[partyKey]*party{}
	i.anchors = anchorVector{}
	i.numbers = nil
	i.records, i.head, i.generation = 0, Record{}, ""
	i.ready, i.failed = false, false
	i.initialReady, i.lastOwnershipChange = false, 0
	i.anchorStart, i.anchorSeen = 0, false
	i.ended, i.started = false, false
	i.active, i.first, i.last = nil, nil, nil
	i.queued, i.captured, i.built = 0, 0, 0
	i.builtHead = Record{}
	i.bootstrap, i.bootSerial = nil, 0
}

// EndReplay validates the captured prefix. It does NOT encode it or launch a
// builder: startup must reach the real HTTP serving door first.
func (i *Index) EndReplay() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.sealCapture()
	i.ended = true
	i.ready = i.records == 0 && !i.failed
	i.initialReady = i.ready
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

func (i *Index) add(u snapshotUnit, keys []partyKey) error {
	ref := i.codec.units
	if err := i.codec.add(u); err != nil {
		return err
	}
	n := sort.Search(i.serials.n, func(n int) bool { return i.serials.get(n) >= u.Position.Msg })
	switch {
	case n == i.serials.n:
		i.serials.add(u.Position.Msg)
		i.latest.add(ref)
	case i.serials.get(n) == u.Position.Msg:
		i.latest.set(n, ref)
	default:
		return errors.New("history conversation serials are out of order")
	}
	for _, key := range keys {
		i.party(key).refs.add(ref)
	}
	return nil
}

// Measurement reports counts, not an estimated heap. Forced-GC paired runner
// measurements must include all vectors, prefix descriptors and codec buffers.
type Measurement struct {
	Units, Records                             uint64
	Blocks, Conversations, Parties, References int
	Prefixes, CompressedBytes, RawTailCapacity int
	Anchors, AnchorCapacity, ScratchCapacity   int
	CapturedUnits, BuiltSerial, QueuedBytes    uint64
	Failed, Ready, Started                     bool
	BootSerial                                 uint64
}

// Measurement reports representation counts without estimating retained heap.
func (i *Index) Measurement() Measurement {
	i.mu.RLock()
	m := Measurement{
		Records: i.records, CapturedUnits: i.captured, BuiltSerial: i.builtHead.Serial,
		QueuedBytes: i.queued, Failed: i.failed, Ready: i.ready, Started: i.started,
		BootSerial: i.bootSerial,
	}
	if i.active != nil {
		m.QueuedBytes += i.active.charge()
	}
	i.mu.RUnlock()
	i.viewMu.RLock()
	defer i.viewMu.RUnlock()
	m.Units, m.Blocks, m.Conversations = i.codec.units, len(i.codec.blocks), i.serials.n
	m.Parties, m.RawTailCapacity = len(i.parties), cap(i.codec.tail)
	m.Anchors, m.AnchorCapacity = i.anchors.n, len(i.anchors.segments)*anchorSegment
	m.ScratchCapacity = cap(i.codec.scratch)
	for _, p := range i.parties {
		m.References += p.refs.n
		m.Prefixes += len(p.inherited)
	}
	for _, b := range i.codec.blocks {
		m.CompressedBytes += len(b.data)
	}
	return m
}
