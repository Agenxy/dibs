package ledger

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// This prototype measures representation, not the production history door.
// Replay has already authenticated the encrypted chain. A second fold observes
// canonical before/after values to build complete compressed snapshots; it
// never substitutes inferred lifecycle rules for core.Apply. Production replay,
// live append wiring, authorization and bounded pagination are NOT implemented
// by this artifact and must still be designed, reviewed and guarded.
type historyProbeMetadata struct {
	From             string    `json:"from"`
	To               string    `json:"to"`
	Type             string    `json:"type"`
	State            string    `json:"state"`
	Consumed         bool      `json:"consumed"`
	AdoptedFrom      string    `json:"adopted_from,omitempty"`
	AdoptedAt        uint64    `json:"adopted_serial,omitempty"`
	Delivered        uint64    `json:"delivered_serial,omitempty"`
	Responded        uint64    `json:"responded_serial,omitempty"`
	Acked            uint64    `json:"acked_serial,omitempty"`
	OutcomeRead      uint64    `json:"outcome_read_serial,omitempty"`
	ReviewRead       uint64    `json:"review_read_serial,omitempty"`
	ReviewCutoff     uint64    `json:"review_cutoff_serial,omitempty"`
	Superseded       uint64    `json:"superseded_by,omitempty"`
	SentAt           time.Time `json:"sent_at,omitzero"`
	DeliveredAt      time.Time `json:"delivered_at,omitzero"`
	TerminalAt       time.Time `json:"terminal_at,omitzero"`
	RetainUntil      time.Time `json:"retain_until,omitzero"`
	Deadline         time.Time `json:"deadline,omitzero"`
	QueuePriority    string    `json:"queue_priority,omitempty"`
	QueueRank        int       `json:"queue_rank,omitempty"`
	QueueDebt        bool      `json:"queue_debt,omitempty"`
	QueueOrderLocked bool      `json:"queue_order_locked,omitempty"`
	QueueChanged     uint64    `json:"queue_changed_serial,omitempty"`
	Milestones       int       `json:"milestone_count,omitempty"`
	Progress         int       `json:"progress_count,omitempty"`
}

func historyProbeMeta(st *core.State, m *core.Message) historyProbeMetadata {
	return historyProbeMetadata{From: m.From, To: m.To, Type: m.Type, State: m.State, Consumed: m.Consumed,
		AdoptedFrom: m.AdoptedFrom, AdoptedAt: m.AdoptedAt, Delivered: m.DeliveredAt, Responded: m.RespondedAt, Acked: m.AckedAt,
		OutcomeRead: m.OutcomeReadAt, ReviewRead: m.ReviewReadAt, ReviewCutoff: st.ReviewReadCutoff, Superseded: m.SupersededBy,
		SentAt: m.SentAt, DeliveredAt: m.DeliveredTime, TerminalAt: m.TerminalAt, RetainUntil: m.RetainUntil, Deadline: m.Deadline,
		QueuePriority: m.QueuePriority, QueueRank: m.QueueRank, QueueDebt: m.QueueDebt, QueueOrderLocked: m.QueueOrderLocked, QueueChanged: m.QueueChangedSerial,
		Milestones: len(m.Milestones), Progress: len(m.Progress)}
}

type historyProbeAuthor struct {
	ID      string `json:"agent"`
	Created uint64 `json:"created_serial,omitempty"`
	Host    string `json:"host,omitempty"`
}

func historyProbeAuthorOf(a *core.Agent) historyProbeAuthor {
	host := ""
	if a.Agent != nil {
		host = a.Agent.HostID
	}
	return historyProbeAuthor{a.ID, a.CreatedSerial, host}
}

type historyProbeAuditUnit struct {
	OpSerial   uint64               `json:"op_serial"`
	MsgSerial  uint64               `json:"msg_serial"`
	Ordinal    uint32               `json:"unit_ordinal"`
	At         time.Time            `json:"at"`
	Kind       string               `json:"kind"`
	Meta       historyProbeMetadata `json:"metadata"`
	Author     historyProbeAuthor   `json:"author"`
	Content    bool                 `json:"has_content,omitempty"`
	AfterKnown bool                 `json:"after_known,omitempty"`
	Evicted    bool                 `json:"evicted,omitempty"`
}

// Fixed chunks count allocation slack, without amortized append capacity
// doubling for million-entry vectors. The design must widen/segment references
// before uint32 overflow; this probe's largest case has only 2M units.
type historyProbeU32 struct {
	Chunks [][]uint32
	N      int
}

func (v *historyProbeU32) add(n uint32) {
	if v.N%4096 == 0 {
		v.Chunks = append(v.Chunks, make([]uint32, 4096))
	}
	v.Chunks[v.N/4096][v.N%4096] = n
	v.N++
}

func (v *historyProbeU32) get(n int) uint32 { return v.Chunks[n/4096][n%4096] }
func (v *historyProbeU32) set(n int, value uint32) {
	v.Chunks[n/4096][n%4096] = value
}

type historyProbeU64 struct {
	Chunks [][]uint64
	N      int
}

func (v *historyProbeU64) add(n uint64) {
	if v.N%4096 == 0 {
		v.Chunks = append(v.Chunks, make([]uint64, 4096))
	}
	v.Chunks[v.N/4096][v.N%4096] = n
	v.N++
}

func (v *historyProbeU64) get(n int) uint64 { return v.Chunks[n/4096][n%4096] }

type historyProbeParty struct {
	ID      string
	Created uint64
}

type historyProbeBlock struct {
	First, LastOp uint64
	Count         int
	RawBytes      int
	Data          []byte
}

type historyCompactProbe struct {
	Blocks  []historyProbeBlock
	Serials historyProbeU64
	Latest  historyProbeU32 // last canonical snapshot for each message, even GC
	Party   map[historyProbeParty]*historyProbeU32
	Pending []historyProbeAuditUnit
	Units   int
	Raw     int
	Tail    []byte // reserved bounded mutable JSON tail for the live observer
}

func (p *historyCompactProbe) add(t *testing.T, u historyProbeAuditUnit, keys []historyProbeParty) {
	t.Helper()
	n := sort.Search(p.Serials.N, func(n int) bool { return p.Serials.get(n) >= u.MsgSerial })
	if n == p.Serials.N {
		p.Serials.add(u.MsgSerial)
		p.Latest.add(uint32(p.Units))
	} else {
		if p.Serials.get(n) != u.MsgSerial {
			t.Fatal("setup: nonmonotone message creation")
		}
		p.Latest.set(n, uint32(p.Units))
	}
	for _, key := range keys {
		refs := p.Party[key]
		if refs == nil {
			refs = &historyProbeU32{}
			p.Party[key] = refs
		}
		refs.add(uint32(p.Units))
	}
	p.Pending = append(p.Pending, u)
	p.Units++
	if len(p.Pending) == 128 {
		p.flush(t)
	}
}

func (p *historyCompactProbe) flush(t *testing.T) {
	t.Helper()
	if len(p.Pending) == 0 {
		return
	}
	raw, err := json.Marshal(p.Pending)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 128<<10 {
		t.Fatal("setup: bounded snapshot block exceeded 128 KiB")
	}
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b := historyProbeBlock{First: uint64(p.Units - len(p.Pending)), LastOp: p.Pending[len(p.Pending)-1].OpSerial, Count: len(p.Pending), RawBytes: len(raw), Data: bytes.Clone(buf.Bytes())}
	decoded := decodeHistoryProbeBlock(t, b)
	if !reflect.DeepEqual(decoded, p.Pending) {
		t.Fatal("setup: compact block failed exact all-field round trip")
	}
	p.Raw += len(raw)
	p.Blocks = append(p.Blocks, b)
	p.Pending = nil // no retained decoded unit objects or compressor buffers
}

func decodeHistoryProbeBlock(t *testing.T, b historyProbeBlock) []historyProbeAuditUnit {
	t.Helper()
	r := flate.NewReader(bytes.NewReader(b.Data))
	defer func() { _ = r.Close() }()
	raw, err := io.ReadAll(io.LimitReader(r, (128<<10)+1))
	if err != nil || len(raw) != b.RawBytes || len(raw) > 128<<10 {
		t.Fatalf("setup: bounded block decode length=%d expected=%d error=%v", len(raw), b.RawBytes, err)
	}
	var units []historyProbeAuditUnit
	if err := json.Unmarshal(raw, &units); err != nil || len(units) != b.Count {
		t.Fatalf("setup: block decode units=%d error=%v", len(units), err)
	}
	return units
}

func (p *historyCompactProbe) unit(t *testing.T, n uint32) historyProbeAuditUnit {
	t.Helper()
	b := sort.Search(len(p.Blocks), func(i int) bool { return p.Blocks[i].First+uint64(p.Blocks[i].Count) > uint64(n) })
	if b == len(p.Blocks) || uint64(n) < p.Blocks[b].First {
		t.Fatal("setup: bad unit reference")
	}
	return decodeHistoryProbeBlock(t, p.Blocks[b])[uint64(n)-p.Blocks[b].First]
}

func (p *historyCompactProbe) receipt() any {
	compressed, refs := 0, 0
	for _, b := range p.Blocks {
		compressed += len(b.Data)
	}
	for _, party := range p.Party {
		refs += party.N
	}
	return struct {
		Units, Blocks, Conversations, PartyReferences, RawSnapshotBytes, CompressedBytes, ReservedTailBytes int
	}{p.Units, len(p.Blocks), p.Serials.N, refs, p.Raw, compressed, cap(p.Tail)}
}

func buildHistoryCompactProbe(t *testing.T, l *Ledger, records int) *historyCompactProbe {
	t.Helper()
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReaderSize(l.f, 1<<20)
	st := core.NewState("million", core.DefaultLimits())
	p := &historyCompactProbe{Party: map[historyProbeParty]*historyProbeU32{}, Tail: make([]byte, 0, 128<<10)}
	for n := 0; n < records; n++ {
		raw, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("setup: compact fold record %d: %v", n, err)
		}
		var rec Line
		if err := json.Unmarshal(raw, &rec); err != nil || rec.Op == nil {
			t.Fatalf("setup: compact fold decode: %v", err)
		}
		if err := l.box.DecryptOp(rec.Op); err != nil {
			t.Fatal(err)
		}
		before := make(map[uint64]historyProbeMetadata, len(st.Messages))
		for serial, m := range st.Messages {
			before[serial] = historyProbeMeta(st, m)
		}
		authors := make(map[string]historyProbeAuthor, len(st.Agents))
		for id, a := range st.Agents {
			authors[id] = historyProbeAuthorOf(a)
		}
		_, events, err := st.Apply(rec.Op, rec.T)
		if err != nil || st.Serial != rec.S {
			t.Fatalf("setup: compact canonical apply serial=%d expected=%d error=%v", st.Serial, rec.S, err)
		}
		observeHistoryCompactProbe(t, p, rec, before, authors, st, events)
	}
	p.flush(t)
	if p.Serials.N != (records-2)/3 || p.Latest.N != p.Serials.N || p.Units != records-4 {
		t.Fatalf("setup: complete projection conversations=%d headers=%d units=%d", p.Serials.N, p.Latest.N, p.Units)
	}
	// Verify the latest canonical header can be recovered after GC, not only
	// that encoded bytes were allocated. Bodies remain in the encrypted ledger.
	for _, n := range []int{0, p.Serials.N / 2, p.Serials.N - 1} {
		u := p.unit(t, p.Latest.get(n))
		if u.MsgSerial != p.Serials.get(n) || u.Meta.From != "lead" || u.Meta.To != "worker" {
			t.Fatal("setup: latest authorization header lookup differs from canonical parties")
		}
	}
	return p
}

func observeHistoryCompactProbe(t *testing.T, p *historyCompactProbe, rec Line, before map[uint64]historyProbeMetadata, authors map[string]historyProbeAuthor, st *core.State, events []core.Event) {
	t.Helper()
	var numbers []uint64
	for n, old := range before {
		if m := st.Messages[n]; m == nil || historyProbeMeta(st, m) != old {
			numbers = append(numbers, n)
		}
	}
	for n := range st.Messages {
		if _, existed := before[n]; !existed {
			numbers = append(numbers, n)
		}
	}
	slices.Sort(numbers)
	author := authors[rec.Op.AgentID]
	if author.ID == "" {
		if a := st.Agents[rec.Op.AgentID]; a != nil {
			author = historyProbeAuthorOf(a)
		} else {
			author.ID = rec.Op.AgentID
		}
	}
	for _, n := range numbers {
		old, existed := before[n]
		m := st.Messages[n]
		meta := old
		if m != nil {
			meta = historyProbeMeta(st, m)
		}
		keys := []historyProbeParty{{meta.From, authors[meta.From].Created}}
		if meta.To != meta.From {
			keys = append(keys, historyProbeParty{meta.To, authors[meta.To].Created})
		}
		ord := uint32(0)
		if existed && m == nil {
			for _, ev := range events {
				serial, _ := ev.Data["msg_serial"].(uint64)
				if serial == n && strings.HasPrefix(ev.Type, "message.") {
					p.add(t, historyProbeAuditUnit{OpSerial: rec.S, MsgSerial: n, Ordinal: ord, At: rec.T, Kind: ev.Type, Meta: old, Author: author}, keys)
					ord++
				}
			}
		}
		kind := rec.Op.Kind
		if m == nil {
			kind = "evicted"
		}
		content := m != nil && ((!existed && rec.Op.Kind == core.OpSendMessage) || (n == rec.Op.MsgSerial && (rec.Op.Kind == core.OpRespond || rec.Op.Kind == core.OpWithdrawMessage)))
		p.add(t, historyProbeAuditUnit{OpSerial: rec.S, MsgSerial: n, Ordinal: ord, At: rec.T, Kind: kind, Meta: meta, Author: author, Content: content, AfterKnown: m != nil, Evicted: m == nil}, keys)
	}
}

func TestHistoryCompactProbeAllFieldsRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 6, 6, 0, 0, 12345, time.UTC)
	meta := historyProbeMetadata{From: "sender", To: "heir", Type: core.MsgRequest, State: core.MsgStateDone, Consumed: true,
		AdoptedFrom: "previous", AdoptedAt: 20, Delivered: 21, Responded: 22, Acked: 23, OutcomeRead: 24, ReviewRead: 25, ReviewCutoff: 26, Superseded: 27,
		SentAt: at, DeliveredAt: at.Add(time.Second), TerminalAt: at.Add(2 * time.Second), RetainUntil: at.Add(time.Hour), Deadline: at.Add(time.Minute),
		QueuePriority: "high", QueueRank: 3, QueueDebt: true, QueueOrderLocked: true, QueueChanged: 28, Milestones: 4, Progress: 2}
	p := &historyCompactProbe{Party: map[historyProbeParty]*historyProbeU32{}}
	u := historyProbeAuditUnit{OpSerial: 30, MsgSerial: 10, Ordinal: 1, At: at, Kind: "message.test", Meta: meta,
		Author: historyProbeAuthor{ID: "heir", Created: 19, Host: "recorded-host"}, Content: true, AfterKnown: true, Evicted: true}
	p.add(t, u, []historyProbeParty{{"sender", 1}, {"heir", 19}})
	p.flush(t)
	if !reflect.DeepEqual(p.unit(t, 0), u) {
		t.Fatal("every accepted metadata and provenance field must survive encoding")
	}
}
