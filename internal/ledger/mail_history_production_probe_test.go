package ledger

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// Separate hosted processes replay the identical encrypted fixture. The
// baseline removes only this derived observer; production uses the default
// reader, one fold, real index, all buffers and forced-GC retained heap.
func TestMailHistoryProductionProbe(t *testing.T) {
	arm := os.Getenv("DIBS_HISTORY_PROBE_ARM")
	if arm == "" {
		t.Skip("hosted-only paired memory and replay wall-time measurement")
	}
	count, err := strconv.Atoi(os.Getenv("DIBS_HISTORY_PROBE_RECORDS"))
	if err != nil || count < 100 {
		t.Fatal("setup: invalid record count")
	}
	mode := os.Getenv("DIBS_HISTORY_PROBE_CASE")
	dir := ".history-production-probe"
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.jsonl")
	if arm == "generate" {
		generateProductionHistory(t, path, box, count, mode)
		return
	}
	if arm != "baseline" && arm != "production" {
		t.Fatal("setup: unknown arm", arm)
	}
	l, err := OpenReadOnly(path, "history-probe", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if arm == "baseline" {
		l.mail = nil
	}
	st := core.NewState("history-probe", core.DefaultLimits())
	started := time.Now()
	n, err := l.Replay(st)
	seconds := time.Since(started).Seconds()
	if err != nil || n != count || st.Serial != uint64(count) {
		t.Fatalf("setup: production replay count=%d serial=%d error=%v", n, st.Serial, err)
	}
	var representation any
	if l.mail != nil {
		representation = l.mail.Measurement()
		if m := l.mail.Measurement(); m.Records != uint64(count) || m.Failed || !m.Ready {
			t.Fatal("setup: observer did not visit every committed record")
		}
	}
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	receipt := map[string]any{
		"arm": arm, "case": mode, "records": n, "heap_alloc_bytes": mem.HeapAlloc,
		"heap_inuse_bytes": mem.HeapInuse, "replay_seconds": seconds, "live_messages": len(st.Messages),
		"live_agents": len(st.Agents), "representation": representation,
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, arm+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("HISTORY_PRODUCTION_MEASUREMENT %s", raw)
	if arm == "production" {
		assertProductionHistoryBudget(t, dir, count, mem.HeapAlloc, seconds)
	}
	runtime.KeepAlive(st)
	runtime.KeepAlive(l)
}

func assertProductionHistoryBudget(t *testing.T, dir string, records int, heap uint64, seconds float64) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "baseline.json"))
	var baseline struct {
		Heap    uint64  `json:"heap_alloc_bytes"`
		Seconds float64 `json:"replay_seconds"`
	}
	if err != nil || json.Unmarshal(raw, &baseline) != nil || baseline.Seconds <= 0 {
		t.Fatal("setup: paired baseline receipt unavailable:", err)
	}
	increment := int64(heap) - int64(baseline.Heap)
	perRecord := float64(increment) / float64(records)
	overhead := seconds/baseline.Seconds - 1
	t.Logf("HISTORY_PRODUCTION_BUDGET increment_bytes=%d bytes_per_record=%.6f replay_overhead_percent=%.3f", increment, perRecord, 100*overhead)
	if perRecord > 48 || float64(increment) > float64(records)*float64(64<<20)/1_000_000 {
		t.Error("production representation exceeds accepted retained-memory ceiling")
	}
	if overhead > 0.15 {
		t.Error("single-fold production replay exceeds the 15 percent overhead target")
	}
}

type productionFixture struct {
	t        *testing.T
	box      *Box
	writer   *bufio.Writer
	state    *core.State
	now      time.Time
	previous string
	limit    uint64
}

func (p *productionFixture) write(op *core.Op) core.Result {
	p.t.Helper()
	if p.state.Serial >= p.limit {
		return nil
	}
	before := p.state.Serial
	result, _, err := p.state.Apply(op, p.now)
	if err != nil || p.state.Serial != before+1 {
		p.t.Fatalf("setup: %s serial=%d before=%d error=%v", op.Kind, p.state.Serial, before, err)
	}
	enc := *op
	if err = p.box.EncryptOp(&enc); err != nil {
		p.t.Fatal(err)
	}
	raw, err := json.Marshal(Line{S: p.state.Serial, T: p.now, N: "history-probe", E: op.Kind, Prev: p.previous, Op: &enc})
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err = p.writer.Write(append(raw, '\n')); err != nil {
		p.t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	p.previous = hex.EncodeToString(sum[:])
	return result
}

func generateProductionHistory(t *testing.T, path string, box *Box, count int, mode string) {
	t.Helper()
	if mode != "simple" && mode != "many" && mode != "rich" && mode != "adopt-merge" {
		t.Fatal("setup: unknown workload", mode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p := productionFixture{
		t: t, box: box, writer: bufio.NewWriterSize(f, 1<<20),
		state: core.NewState("history-probe", core.DefaultLimits()), now: t0, limit: uint64(count),
	}
	parties := 2
	if mode != "simple" {
		parties = 50
	}
	ids := make([]string, parties)
	for n := range ids {
		ids[n] = fmt.Sprintf("party-%02d", n)
		p.write(&core.Op{
			Kind: core.OpRegister, Name: ids[n], NewToken: ids[n], PID: 1,
			Nonce:     "production-history-fixture-nonce-" + ids[n],
			AgentKind: core.KindPersistent, V7Semantics: true,
			Agent: &core.AgentInfo{HostID: "fixture-host-" + strconv.Itoa(n), CWD: "/fixture/never-opened"},
		})
		p.write(&core.Op{Kind: core.OpAckBoard, Token: ids[n]})
	}
	cycle := 0
	for p.state.Serial < p.limit {
		p.cycle(ids, cycle, mode)
		slices.Sort(ids)
		ids = slices.Compact(ids)
		cycle++
	}
	if err = p.writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err = f.Sync(); err != nil {
		t.Fatal(err)
	}
	t.Logf("generated %d identical encrypted %s records", p.state.Serial, mode)
}

func (p *productionFixture) cycle(ids []string, cycle int, mode string) {
	from, to := ids[cycle%len(ids)], ids[(cycle+1)%len(ids)]
	send := &core.Op{
		Kind: core.OpSendMessage, Token: from, To: to, MsgType: core.MsgNotify,
		Body: "quoted content stays solely in the existing encrypted ledger",
	}
	if mode == "rich" {
		send.MsgType, send.DeadlineSec = core.MsgRequest, 600
		send.Choices = []string{"Accept quoted evidence", "Decline quoted evidence"}
		send.Attachments = []core.Attachment{{Path: "/fixture/never-read/" + strconv.Itoa(cycle)}}
	}
	result := p.write(send)
	if result == nil {
		return
	}
	serial := result["msg_serial"].(uint64)
	if mode == "adopt-merge" && p.state.Serial+3 <= p.limit {
		if p.state.Agents[to].Status == core.StatusActive || p.state.Agents[to].Status == core.StatusStale {
			p.write(&core.Op{Kind: core.OpSweep, DeadAgents: []string{to}, V7Semantics: true})
		}
		heir := ids[(cycle+2)%len(ids)]
		p.write(&core.Op{Kind: core.OpAdoptAgent, Token: heir, To: to, AdoptAuthorised: true, V7Semantics: true})
		previous := to
		to = heir
		if cycle%100 == 0 && len(ids) > 3 {
			p.write(&core.Op{Kind: core.OpMergeAgents, To: ids[(cycle+1)%len(ids)], MergeInto: heir})
			// Replace a retired spelling in future traffic by its actual survivor.
			ids[(cycle+1)%len(ids)] = heir
		} else {
			// The worker returns after its one-time recovery. Leaving all old
			// workers dormant for years of fixture time eventually archives
			// their tokens, so the setup stops testing repeated live recovery.
			p.write(&core.Op{Kind: core.OpWake, Token: previous})
		}
	}
	if mode == "rich" {
		p.write(&core.Op{Kind: core.OpRespond, Token: to, MsgSerial: serial, Disposition: "approve", Milestones: []string{strings.Repeat("recorded label ", 5)}})
		p.write(&core.Op{Kind: core.OpRespond, Token: to, MsgSerial: serial, Disposition: "done", Body: "recorded result", Deliverable: "/fixture/never-opened"})
	} else {
		p.write(&core.Op{Kind: core.OpAckMessage, Token: to, MsgSerial: serial})
	}
	p.now = p.now.Add(20 * time.Minute)
	p.write(&core.Op{Kind: core.OpSweep, V7Semantics: true})
}
