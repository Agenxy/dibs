package ledger

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

const historyProbeRecords = 1_000_000

// This is a design measurement, not a history implementation. All arms replay
// the same encrypted fixture through Ledger.Replay. The packed header arm is an
// OPTIMISTIC FLOOR: it omits unit metadata, content, per-party references and
// most of a working index. Passing it would not prove the API is implementable;
// failing it rules out retaining one fixed-width authority header per message.
// The fixture has no adoption/merge, so it cannot validate those semantics.
func TestMailHistoryMemoryDesignProbe(t *testing.T) {
	arm := os.Getenv("DIBS_HISTORY_PROBE_ARM")
	if arm == "" {
		t.Skip("hosted-only isolated design measurement")
	}
	// Each hosted job has a fresh checkout. Use a fixed package-local fixture
	// directory across its separate processes, never an environment-owned path
	// passed into production key or ledger readers.
	const dir = ".history-design-probe"
	records := historyProbeCount(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.jsonl")
	if arm == "generate" {
		generateHistoryProbe(t, path, box, records)
		return
	}
	if arm != "baseline" && arm != "sparse" && arm != "headers" && arm != "compact" {
		t.Fatalf("setup: unknown arm %q", arm)
	}
	l, err := OpenReadOnly(path, "million", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	st := core.NewState("million", core.DefaultLimits())
	var headers []historyProbeHeader
	if arm == "headers" {
		// Exact fixture capacity avoids charging slice growth to the floor.
		headers = make([]historyProbeHeader, 0, (records-2)/3)
		l.OnEvents = func(events []core.Event) {
			for _, ev := range events {
				if ev.Type != "message.sent" {
					continue
				}
				n := ev.Serial // message.sent uses its own serial, not a parent field.
				m := st.Messages[n]
				if n != st.Serial || m == nil || m.From != "lead" || m.To != "worker" || m.AdoptedFrom != "" {
					t.Fatal("setup: canonical sent header does not match the fixture")
				}
				headers = append(headers, historyProbeHeader{Serial: n, From: 1, To: 2})
			}
		}
	}
	started := time.Now()
	count, err := l.Replay(st)
	if err != nil || count != records || st.Serial != uint64(records) {
		t.Fatalf("setup: production replay count=%d serial=%d error=%v", count, st.Serial, err)
	}
	expectedMail := 0
	if (records-4)%3 != 0 {
		expectedMail = 1
	}
	if len(st.Agents) != 2 || len(st.Messages) != expectedMail {
		t.Fatalf("setup: unexpected final coordination state: agents=%d messages=%d", len(st.Agents), len(st.Messages))
	}
	var seeks []historyProbeSeek
	if arm != "baseline" {
		seeks = historyProbeSeeks(t, l, records)
	}
	if arm == "headers" && len(headers) != (records-2)/3 {
		t.Fatalf("setup: expected %d canonical headers, got %d", (records-2)/3, len(headers))
	}
	// These two coalesced ranges are only the fixture's party lookup floor.
	// Arbitrary interleavings and inherited histories need additional structure.
	var ranges []historyProbeRange
	if arm != "baseline" {
		ranges = []historyProbeRange{{Party: 1, Created: 1, First: 5, Last: uint64(records)}, {Party: 2, Created: 3, First: 5, Last: uint64(records)}}
	}
	var compact *historyCompactProbe
	if arm == "compact" {
		compact = buildHistoryCompactProbe(t, l, records)
	}
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	receipt := struct {
		Arm           string  `json:"arm"`
		Records       int     `json:"records"`
		HeapAlloc     uint64  `json:"heap_alloc_bytes"`
		HeapInuse     uint64  `json:"heap_inuse_bytes"`
		ReplaySeconds float64 `json:"replay_and_index_seconds"`
		LiveMessages  int     `json:"live_messages"`
		Headers       int     `json:"headers"`
		HeaderCap     int     `json:"header_capacity"`
		Seeks         int     `json:"seeks"`
		SeekCap       int     `json:"seek_capacity"`
		Ranges        int     `json:"party_ranges"`
		Compact       any     `json:"compact,omitempty"`
	}{arm, count, mem.HeapAlloc, mem.HeapInuse, time.Since(started).Seconds(), len(st.Messages), len(headers), cap(headers), len(seeks), cap(seeks), len(ranges), nil}
	if compact != nil {
		receipt.Compact = compact.receipt()
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, arm+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("HISTORY_DESIGN_MEASUREMENT %s", raw)
	runtime.KeepAlive(st)
	runtime.KeepAlive(l)
	runtime.KeepAlive(headers)
	runtime.KeepAlive(seeks)
	runtime.KeepAlive(ranges)
	runtime.KeepAlive(compact)
}

func historyProbeCount(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("DIBS_HISTORY_PROBE_RECORDS")
	if raw == "" {
		return historyProbeRecords
	}
	n, err := strconv.Atoi(raw)
	if err != nil || (n != 100_000 && n != 1_000_000 && n != 2_000_000) {
		t.Fatalf("setup: unsupported record count %q", raw)
	}
	return n
}

// 32 bytes per message without Go pointers, metadata or duplicated party refs.
type historyProbeHeader struct {
	Serial, AdoptedAt               uint64
	From, To, AdoptedFrom, Reserved uint32
}

// A sparse validated ledger block anchor, at most 4096 records apart. The next
// anchor commits to this block's end; no individual record object is retained.
type historyProbeSeek struct {
	Serial uint64
	Offset int64
	Prev   [32]byte
}

type historyProbeRange struct {
	Created, First, Last uint64
	Party                uint32
}

func historyProbeSeeks(t *testing.T, l *Ledger, records int) []historyProbeSeek {
	t.Helper()
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReaderSize(l.f, 1<<20)
	seeks := make([]historyProbeSeek, 0, (records+4095)/4096)
	var offset int64
	var prev [32]byte
	for n := 0; n < records; n++ {
		raw, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("setup: sparse pass at record %d: %v", n, err)
		}
		var rec Line
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		expected := ""
		if n > 0 {
			expected = hex.EncodeToString(prev[:])
		}
		if rec.S != uint64(n+1) || rec.Prev != expected {
			t.Fatalf("setup: sparse pass chain/serial mismatch at record %d", n)
		}
		if n%4096 == 0 {
			seeks = append(seeks, historyProbeSeek{Serial: rec.S, Offset: offset, Prev: prev})
		}
		prev = sha256.Sum256(raw[:len(raw)-1])
		offset += int64(len(raw))
	}
	return seeks
}

func generateHistoryProbe(t *testing.T, path string, box *Box, records int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	s := core.NewState("million", core.DefaultLimits())
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prev := ""
	write := func(op *core.Op) core.Result {
		t.Helper()
		before := s.Serial
		r, _, err := s.Apply(op, now)
		if err != nil || s.Serial != before+1 {
			t.Fatalf("setup: %s serial=%d before=%d error=%v", op.Kind, s.Serial, before, err)
		}
		enc := *op
		if err := box.EncryptOp(&enc); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(Line{S: s.Serial, T: now, N: "million", E: op.Kind, Prev: prev, Op: &enc})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(raw)
		prev = hex.EncodeToString(h[:])
		return r
	}
	for _, id := range []string{"lead", "worker"} {
		write(&core.Op{Kind: core.OpRegister, Name: id, NewToken: id, PID: 1, V7Semantics: true})
		write(&core.Op{Kind: core.OpAckBoard, Token: id})
	}
	for s.Serial < uint64(records) {
		n := write(&core.Op{Kind: core.OpSendMessage, Token: "lead", To: "worker", MsgType: core.MsgNotify, Body: "one authored field in the encrypted ledger"})["msg_serial"].(uint64)
		if s.Serial == uint64(records) {
			break
		}
		write(&core.Op{Kind: core.OpAckMessage, Token: "worker", MsgSerial: n})
		if s.Serial == uint64(records) {
			break
		}
		now = now.Add(20 * time.Minute)
		write(&core.Op{Kind: core.OpSweep, V7Semantics: true})
	}
	if s.Serial != uint64(records) {
		t.Fatalf("setup: generated serial %d", s.Serial)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("generated %d encrypted records once for all arms", s.Serial)
}
