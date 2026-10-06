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
	defer i.mu.Unlock()
	if i.failed {
		return
	}
	if i.records%4096 == 0 {
		i.anchors = append(i.anchors, Anchor{rec.Serial, rec.Offset, rec.Prev})
	}
	if i.records == 0 {
		i.generation = hex.EncodeToString(rec.Hash[:])
	}
	i.records++
	i.head = rec
	numbers := i.numbers[:0]
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
	i.numbers = numbers[:0]
	author := before.Authors[op.AgentID]
	if author.ID == "" {
		if a := st.Agents[op.AgentID]; a != nil {
			author = authorOf(a)
		} else {
			author.ID = op.AgentID
		}
	}
	for _, serial := range numbers {
		i.observeMessage(rec, before, st, op, events, serial, author)
		if i.failed {
			return
		}
	}
}

func (i *Index) observeMessage(
	rec Record, before Snapshot, st *core.State, op *core.Op, events []core.Event, serial uint64, author Author,
) {
	old, existed := before.Mail[serial]
	m := st.Messages[serial]
	meta := old
	if m != nil {
		meta = stateMetadata(st, m)
	}
	from, to := before.key(meta.From, st), before.key(meta.To, st)
	keys := []partyKey{from}
	if to != from {
		keys = append(keys, to)
	}
	if existed {
		i.inheritMovedParty(old.From, meta.From, before, st)
		i.inheritMovedParty(old.To, meta.To, before, st)
	}
	ordinal := uint32(0)
	if existed && m == nil {
		for _, ev := range events {
			n, _ := ev.Data["msg_serial"].(uint64)
			if n == serial && strings.HasPrefix(ev.Type, "message.") {
				i.add(snapshotUnit{
					Position: position{rec.Serial, serial, ordinal}, At: rec.At,
					Kind: ev.Type, Metadata: old, Author: author,
				}, keys)
				ordinal++
			}
		}
	}
	kind := op.Kind
	if m == nil {
		kind = "evicted"
	}
	content := m != nil && ((!existed && op.Kind == core.OpSendMessage) ||
		(serial == op.MsgSerial && (op.Kind == core.OpRespond || op.Kind == core.OpWithdrawMessage)))
	i.add(snapshotUnit{
		Position: position{rec.Serial, serial, ordinal}, At: rec.At,
		Kind: kind, Metadata: meta, Author: author, Content: content, AfterKnown: m != nil, Evicted: m == nil,
	}, keys)
}

func (i *Index) inheritMovedParty(old, current string, before Snapshot, st *core.State) {
	if old == "" || old == current {
		return
	}
	destination := i.party(before.key(current, st))
	source := i.parties[before.key(old, st)]
	if !destination.inherit(source) {
		i.failed = true
	}
}
