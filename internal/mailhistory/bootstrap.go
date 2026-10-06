package mailhistory

import (
	"context"
	"errors"

	"github.com/agenxy/dibs/internal/core"
)

// ConfigureBootstrap records only the validated boot boundary and a reader.
// No record, snapshot or shadow State is retained or visited during Replay.
// Live commits after this boundary enter the bounded queue even before Accept.
func (i *Index) ConfigureBootstrap(head Record, records uint64, read func(context.Context, *Index) error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.started {
		i.failCapture()
		return
	}
	i.head, i.records, i.bootstrap = head, records, read
	i.ended, i.ready = true, records == 0
	i.queueLimit = liveQueueBytes
}

// ReplayProjector belongs exclusively to the background reader. Its scratch
// has no canonical State pointer, and the writer never accesses this object.
type ReplayProjector struct {
	index   *Index
	numbers []uint64
}

// ReplayProjector returns independent background scratch, never writer scratch.
func (i *Index) ReplayProjector() *ReplayProjector { return &ReplayProjector{index: i} }

// Observe encodes one validated shadow transition directly into bounded codec
// blocks. Boot snapshots never accumulate in the writer's raw queue.
func (p *ReplayProjector) Observe(ctx context.Context, ordinal uint64, rec Record,
	before Snapshot, st *core.State, op *core.Op, events []core.Event,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i := p.index
	i.mu.RLock()
	failed := i.failed
	i.mu.RUnlock()
	if failed {
		return errors.New("history view was invalidated during bootstrap")
	}
	i.viewMu.Lock()
	defer i.viewMu.Unlock()
	if ordinal%4096 == 0 {
		i.anchors = append(i.anchors, Anchor{rec.Serial, rec.Offset, rec.Prev})
	}
	if ordinal == 0 {
		// Observe on the live writer cannot set this on a nonempty boot ledger.
		i.mu.Lock()
		i.generation = generationOf(rec)
		i.mu.Unlock()
	}
	return projectChanges(rec, before, st, op, events, &p.numbers, p.emit, p.inherit)
}

func (p *ReplayProjector) emit(u snapshotUnit) error {
	keys := [2]partyKey{{u.Metadata.From, u.FromCreated}, {u.Metadata.To, u.ToCreated}}
	count := 2
	if keys[0] == keys[1] {
		count = 1
	}
	return p.index.add(u, keys[:count])
}

func (p *ReplayProjector) inherit(from, to partyKey) error {
	if !p.index.party(to).inherit(p.index.parties[from]) {
		return errors.New("history inherited source bound exceeded")
	}
	return nil
}

// Progress publishes scalars only; the private shadow never enters the Index.
func (p *ReplayProjector) Progress(rec Record) {
	p.index.mu.Lock()
	p.index.builtHead = rec
	p.index.mu.Unlock()
}

// Finish records the canonical S0 canary without retaining the shadow State.
func (p *ReplayProjector) Finish(rec Record, stateHash [32]byte) {
	p.index.mu.Lock()
	p.index.builtHead = rec
	p.index.bootSerial, p.index.bootHash = rec.Serial, stateHash
	p.index.mu.Unlock()
}
