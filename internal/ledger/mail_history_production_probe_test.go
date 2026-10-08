// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// Separate hosted processes replay the identical encrypted fixture. The
// baseline removes only this derived observer; production uses the default
// reader, one boot fold followed by the private post-serving fold, real index,
// all buffers and forced-GC retained heap. Warming peak includes the shadow.
func TestMailHistoryProductionProbe(t *testing.T) {
	arm := os.Getenv("DIBS_HISTORY_PROBE_ARM")
	if arm == "" {
		t.Skip("hosted-only paired memory and replay wall-time measurement")
	}
	count, err := strconv.Atoi(os.Getenv("DIBS_HISTORY_PROBE_RECORDS"))
	if err != nil || count < 0 || (count > 0 && count < 100) {
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
	baselineArm := arm == "baseline" || arm == "baseline-repeat-1" || arm == "baseline-repeat-2"
	if !baselineArm && arm != "production" {
		t.Fatal("setup: unknown arm", arm)
	}
	// Arms run sequentially in separate processes; each replaces this private
	// copy from the immutable source before timing. No environment value names
	// a path passed into production Open.
	path = copyHistoryProbeLedger(t, path, filepath.Join(dir, "paired.ledger.jsonl"))
	l, err := Open(path, "history-probe", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if baselineArm {
		l.mail = nil
	}
	st := core.NewState("history-probe", core.DefaultLimits())
	started := time.Now()
	n, err := l.Replay(st)
	replaySeconds := time.Since(started).Seconds()
	if err != nil || n != count || st.Serial != uint64(count) {
		t.Fatalf("setup: production replay count=%d serial=%d error=%v", n, st.Serial, err)
	}
	var representation any
	if l.mail != nil {
		representation = l.mail.Measurement()
		if m := l.mail.Measurement(); m.Records != uint64(count) || m.Failed || m.Started || m.Units != 0 || m.Blocks != 0 || m.CapturedUnits != 0 || m.QueuedBytes != 0 {
			t.Fatal("setup: boot replay performed history work or lost its boundary")
		}
	}
	liveMessages, liveAgents := len(st.Messages), len(st.Agents)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := engine.New(st, l, nil)
	joined := make(chan struct{})
	go func() { eng.Run(ctx); close(joined) }()
	defer func() { cancel(); <-joined }()
	if _, _, err := eng.SubscribeInfo(ctx, ""); err != nil {
		t.Fatal("setup: writer did not reach serving:", err)
	}
	bootSeconds := time.Since(started).Seconds()
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	captureHeap := mem.HeapAlloc
	// Enter the same net/http Accept boundary as dibd. Starting the builder
	// before this boundary would buy boot timing by measuring the wrong door.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if l.mail != nil {
		srv.Listener = l.mail.ServingListener(ctx, srv.Listener)
	}
	servingAt := time.Now()
	srv.Start()
	defer srv.Close()
	res, serveErr := srv.Client().Get(srv.URL)
	if serveErr != nil {
		t.Fatal("setup: serving boundary:", serveErr)
	}
	_ = res.Body.Close()
	seconds := bootSeconds + time.Since(servingAt).Seconds()
	var warmSeconds, p99 float64
	peak := captureHeap
	if l.mail != nil {
		warmSeconds, peak, p99 = measureProductionWarm(t, ctx, eng, l, peak)
		representation = l.mail.Measurement()
	}
	runtime.GC()
	runtime.ReadMemStats(&mem)
	receipt := map[string]any{
		"arm": arm, "case": mode, "records": n, "heap_alloc_bytes": mem.HeapAlloc,
		"heap_inuse_bytes": mem.HeapInuse, "replay_seconds": seconds, "capture_replay_seconds": replaySeconds,
		"boot_replay_seconds": replaySeconds,
		"live_messages":       liveMessages, "live_agents": liveAgents, "representation": representation,
		"capture_heap_bytes": captureHeap, "warming_peak_heap_bytes": peak,
		"warm_seconds": warmSeconds, "warming_coordination_p99_seconds": p99,
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
		baseline := readProductionProbeArm(t, dir, "baseline")
		proportional := float64(count) * float64(64<<20) / 1_000_000
		allowance := max(proportional, float64(8<<20))
		above := float64(peak) - float64(mem.HeapAlloc)
		t.Logf("HISTORY_WARM_PEAK_BOUND above_steady_bytes=%.0f proportional_bytes=%.0f "+
			"floor_bytes=%d allowance_bytes=%.0f proportional_pass=%t accepted_pass=%t",
			above, proportional, 8<<20, allowance, above <= proportional, above <= allowance)
		// Architect58415 applies the previously stated56771 fixed working-set
		// floor. Preserve the proportional verdict alongside the fixed-cost steady bound.
		if count > 0 && above > allowance {
			t.Error("warming peak exceeds steady heap plus max(64 MiB per million records, 8 MiB)")
		}
		t.Logf("HISTORY_WARMING_CAPTURE capture_increment_bytes=%d peak_increment_bytes=%d warm_seconds=%.9f coordination_p99_seconds=%.9f", int64(captureHeap)-int64(baseline.Heap), int64(peak)-int64(baseline.Heap), warmSeconds, p99)
	}
	runtime.KeepAlive(st)
	runtime.KeepAlive(l)
	runtime.KeepAlive(eng)
}

func assertProductionHistoryBudget(t *testing.T, dir string, records int, heap uint64, seconds float64) {
	t.Helper()
	baseline := readProductionProbeArm(t, dir, "baseline")
	first := readProductionProbeArm(t, dir, "baseline-repeat-1")
	second := readProductionProbeArm(t, dir, "baseline-repeat-2")
	spread := max(baseline.Seconds, first.Seconds, second.Seconds) - min(baseline.Seconds, first.Seconds, second.Seconds)
	t.Logf("HISTORY_BASELINE_NOISE original_seconds=%.9f repeat1_seconds=%.9f repeat2_seconds=%.9f spread_seconds=%.9f spread_percent=%.3f", baseline.Seconds, first.Seconds, second.Seconds, spread, 100*spread/baseline.Seconds)
	increment := int64(heap) - int64(baseline.Heap)
	if records == 0 {
		t.Logf("HISTORY_PRODUCTION_EMPTY incremental_bytes=%d replay_delta_seconds=%.9f; lazy codec buffers have not been allocated", increment, seconds-baseline.Seconds)
		return
	}
	perRecord := float64(increment) / float64(records)
	overhead := seconds/baseline.Seconds - 1
	t.Logf("HISTORY_PRODUCTION_BUDGET increment_bytes=%d bytes_per_record=%.6f replay_delta_seconds=%.9f replay_overhead_percent=%.3f", increment, perRecord, seconds-baseline.Seconds, 100*overhead)
	// Architect58817: fixed codec working storage is included in forced-GC
	// heap, with at most 4 MiB allowance. At one million records and above
	// the entire measured increment, including that fixed cost, must fit 48B/N.
	allowance := int64(4 << 20)
	if records >= 1_000_000 {
		allowance = 0
	}
	bound := int64(records)*48 + allowance
	t.Logf("HISTORY_STEADY_BOUND original_48_pass=%t fixed_allowance_bytes=%d accepted_bound_bytes=%d accepted_pass=%t", perRecord <= 48, allowance, bound, increment <= bound)
	if increment > bound {
		t.Error("production representation exceeds accepted retained-memory ceiling")
	}
	if records >= 1_000_000 && overhead > 0.15 {
		t.Error("single-fold production replay exceeds the 15 percent overhead target")
	}
}

type productionProbeArm struct {
	Heap    uint64  `json:"heap_alloc_bytes"`
	Seconds float64 `json:"replay_seconds"`
}

func readProductionProbeArm(t *testing.T, dir, arm string) productionProbeArm {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, arm+".json"))
	var result productionProbeArm
	if err != nil || json.Unmarshal(raw, &result) != nil || result.Seconds <= 0 {
		t.Fatal("setup: paired baseline receipt unavailable:", arm, err)
	}
	return result
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
	if mode != "simple" && mode != "many" && mode != "rich" && mode != "adopt-merge" && mode != "large-body" {
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
	if mode != "simple" && mode != "large-body" {
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
	if mode == "large-body" {
		send.Body = strings.Repeat("b", 32<<10)
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
