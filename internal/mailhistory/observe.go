package mailhistory

import (
	"encoding/hex"
	"slices"
	"strings"

	"github.com/agenxy/dibs/internal/core"
)

// Observe receives the real before/after values and fold events only after
// successful append, or after replay validated that record. It never applies
// an op, invents a transition, or performs I/O. A failed derived view leaves
// normal coordination usable and must fail history queries closed.
func (i *Index) Observe(rec Record, before Snapshot, st *core.State, op *core.Op, events []core.Event) {
	i.mu.Lock()
	defer func() {
		i.mu.Unlock()
		select {
		case i.signal <- struct{}{}:
		default:
		}
	}()
	if i.failed {
		return
	}
	if ownershipChanged(before, st) {
		i.lastOwnershipChange = rec.Serial
	}
	if !i.anchorSeen || i.records%4096 == 0 || rec.End-i.anchorStart > anchorBytes {
		i.ensureCapture()
		i.active.anchors = append(i.active.anchors, Anchor{rec.Serial, rec.Offset, rec.Prev})
		i.anchorStart, i.anchorSeen = rec.Offset, true
	}
	if i.records == 0 {
		i.generation = generationOf(rec)
	}
	i.records++
	i.head = rec
	i.ready = false
	err := projectChanges(rec, before, st, op, events, &i.numbers, i.captureUnit, i.captureMove)
	if err != nil || (i.active != nil && i.queueLimit > 0 && i.queued+i.active.charge() > i.queueLimit) {
		i.failCapture()
	}
}

func ownershipChanged(before Snapshot, st *core.State) bool {
	for serial, old := range before.Mail {
		if m := st.Messages[serial]; m != nil &&
			(m.From != old.From || m.To != old.To || m.AdoptedFrom != old.AdoptedFrom || m.AdoptedAt != old.AdoptedAt) {
			return true
		}
	}
	return false
}

func generationOf(rec Record) string { return hex.EncodeToString(rec.Hash[:]) }

// Both folds project their canonical before/after values through this function.
// The live writer captures a bounded queue; the private background fold encodes
// directly. Neither path invents state transitions or keeps authored content.
func projectChanges(rec Record, before Snapshot, st *core.State, op *core.Op, events []core.Event,
	scratch *[]uint64, emit func(snapshotUnit) error, inherit func(partyKey, partyKey) error,
) error {
	numbers := (*scratch)[:0]
	for serial, old := range before.Mail {
		if m := st.Messages[serial]; m == nil || stateMetadata(st, m) != old {
			numbers = append(numbers, serial)
		}
	}
	for serial := range st.Messages {
		if _, existed := before.Mail[serial]; !existed {
			numbers = append(numbers, serial)
		}
	}
	slices.Sort(numbers)
	*scratch = numbers[:0]
	author := before.Authors[op.AgentID]
	if author.ID == "" {
		if a := st.Agents[op.AgentID]; a != nil {
			author = authorOf(a)
		} else {
			author.ID = op.AgentID
		}
	}
	for _, serial := range numbers {
		if err := projectMessage(rec, before, st, op, events, serial, author, emit, inherit); err != nil {
			return err
		}
	}
	return nil
}

func projectMessage(
	rec Record, before Snapshot, st *core.State, op *core.Op, events []core.Event, serial uint64, author Author,
	emit func(snapshotUnit) error, inherit func(partyKey, partyKey) error,
) error {
	old, existed := before.Mail[serial]
	m := st.Messages[serial]
	meta := old
	if m != nil {
		meta = stateMetadata(st, m)
	}
	from, to := before.key(meta.From, st), before.key(meta.To, st)
	if existed {
		if err := inheritParties(old, meta, before, st, inherit); err != nil {
			return err
		}
	}
	ordinal := uint32(0)
	if existed && m == nil {
		for _, ev := range events {
			n, _ := ev.Data["msg_serial"].(uint64)
			if n == serial && strings.HasPrefix(ev.Type, "message.") {
				if err := emit(snapshotUnit{
					Position: position{rec.Serial, serial, ordinal}, At: rec.At,
					Kind: ev.Type, Metadata: old, Author: author,
					FromCreated: from.Created, ToCreated: to.Created,
				}); err != nil {
					return err
				}
				ordinal++
			}
		}
	}
	kind := op.Kind
	if m == nil {
		kind = "evicted"
	}
	return emit(snapshotUnit{
		Position: position{rec.Serial, serial, ordinal}, At: rec.At,
		Kind: kind, Metadata: meta, Author: author, Content: hasContent(op, serial, existed, m != nil),
		AfterKnown: m != nil, Evicted: m == nil,
		FromCreated: from.Created, ToCreated: to.Created,
	})
}

func inheritParties(
	old, current Metadata, before Snapshot, st *core.State, inherit func(partyKey, partyKey) error,
) error {
	for _, pair := range [][2]string{{old.From, current.From}, {old.To, current.To}} {
		if pair[0] != "" && pair[0] != pair[1] {
			if err := inherit(before.key(pair[0], st), before.key(pair[1], st)); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasContent(op *core.Op, serial uint64, existed, present bool) bool {
	return present && ((!existed && op.Kind == core.OpSendMessage) ||
		(serial == op.MsgSerial && (op.Kind == core.OpRespond || op.Kind == core.OpWithdrawMessage)))
}

func (i *Index) captureMove(from, to partyKey) error {
	i.ensureCapture()
	i.active.moves = append(i.active.moves, rawMove{
		before: i.active.count, from: from, to: to,
	})
	return nil
}
