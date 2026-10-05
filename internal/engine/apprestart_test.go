package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/harnessenv"
)

func TestCoordinatorRestartSettingIsLedgeredAndOtherSettingsStayAdminOnly(t *testing.T) {
	e, ctx, _, worker := configureBoard(t)
	listing, err := e.Configure(ctx, worker, "", "")
	if err != nil {
		t.Fatal(err)
	}
	find := func(result core.Result, key string) map[string]any {
		for _, row := range result["settings"].([]map[string]any) {
			if row["setting"] == key {
				return row
			}
		}
		return nil
	}
	if row := find(listing, core.RestartResumeSetting); row["value"] != "0s" || row["set_by"] != "default" {
		t.Fatalf("unconfigured restart source = %v", row)
	}
	if row := find(listing, core.RestartIntervalSetting); row["value"] != "2s" || row["set_by"] != "default" {
		t.Fatalf("unconfigured restart interval = %v", row)
	}
	onLoop(t, ctx, e, func(st *core.State) { st.Agents["hand"].Role = core.RoleCoordinator })
	if _, err := e.Configure(ctx, worker, "wake.sockets", "false"); err == nil {
		t.Fatal("coordinator changed an ordinary admin setting")
	}
	res, err := e.Configure(ctx, worker, core.RestartResumeSetting, "1h")
	if err != nil || res["saved"] != true {
		t.Fatalf("coordinator restart setting = %v, %v", res, err)
	}
	got, err := e.query(ctx, func() core.Result { return core.Result{"entry": e.state.RestartSettings[core.RestartResumeSetting]} })
	if err != nil || got["entry"].(core.RestartSetting).By != "hand" {
		t.Fatalf("ledgered provenance = %v, %v", got, err)
	}
	listing, err = e.Configure(ctx, worker, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if row := find(listing, core.RestartResumeSetting); row["value"] != "1h" ||
		row["set_by"] != "hand" || row["set_at"] == nil {
		t.Fatalf("ledger override source = %v", row)
	}
	if _, err := e.Configure(ctx, worker, core.RestartIntervalSetting, "0s"); err == nil {
		t.Fatal("zero pacing interval accepted")
	}
}

func TestAppRestartSnapshotsRecentLocalChatGPTWorkAndInboxReadsItOnce(t *testing.T) {
	e, ctx, _, worker := configureBoard(t)
	e.SetWakeCommands(map[string]WakeCommand{"codex": {Argv: []string{"codex", "queue", "--thread", "{thread}", "--message", "{message}"}}})
	if _, err := e.Configure(ctx, worker, core.RestartResumeSetting, "1h"); err == nil {
		t.Fatal("member enabled the restart sweep")
	}
	onLoop(t, ctx, e, func(st *core.State) { st.Agents["hand"].Role = core.RoleCoordinator })
	if _, err := e.Configure(ctx, worker, core.RestartResumeSetting, "1h"); err != nil {
		t.Fatal(err)
	}
	thread := "12345678-1234-1234-1234-123456789abc"
	now := time.Now().UTC()
	fullText := strings.Repeat("世界", 400)
	onLoop(t, ctx, e, func(st *core.State) {
		l := st.Agents["hand"]
		l.Agent = &core.AgentInfo{Harness: "codex", Surface: harnessenv.ChatGPTApp, HostID: st.NodeID}
		l.CurrentSession = thread
		l.LastCoordination = now.Add(-30 * time.Minute)
		l.Slots = map[string]core.Slot{
			"s1": {ID: "s1", Text: fullText, UpdatedSerial: 101},
			"s2": {ID: "s2", Text: "old second task", UpdatedSerial: 102},
			"s3": {ID: "s3", Text: "clear this task", UpdatedSerial: 103},
		}
	})
	var plans []wakePlan
	_, err := e.query(ctx, func() core.Result {
		_, _, _ = e.observeAppRestart("", "app:1", now, nil) // initial observation is baseline only
		before := e.snapshotAppRestart(now)
		e.state.Agents["hand"].Slots["s2"] = core.Slot{ID: "s2", Text: "post-restart change", UpdatedSerial: 104}
		delete(e.state.Agents["hand"].Slots, "s3")
		var observeErr error
		plans, _, observeErr = e.observeAppRestart("app:1", "app:2", now.Add(time.Minute), before)
		return core.Result{"error": observeErr}
	})
	if err != nil || len(plans) != 1 {
		t.Fatalf("restart plans = %d, err %v", len(plans), err)
	}
	argv := strings.Join(plans[0].argv, "|")
	if !strings.Contains(argv, "app restarted") || strings.Contains(argv, fullText) || strings.Contains(argv, "hand") {
		t.Fatalf("queue argv carried a declaration or name: %q", argv)
	}
	inbox, err := e.Inbox(ctx, worker)
	if err != nil {
		t.Fatal(err)
	}
	updates, _ := inbox["agent_updates"].([]string)
	joined := strings.Join(updates, "\n")
	if len(updates) == 0 || !strings.Contains(joined, fullText) || !strings.Contains(joined, "Before the restart") {
		t.Fatalf("unchanged declaration was not quoted in full: %v", inbox)
	}
	if !strings.Contains(joined, "Updated since the restart (slot s2): post-restart change") ||
		strings.Contains(joined, "old second task") {
		t.Fatalf("changed declaration was presented as an old quote: %v", updates)
	}
	if !strings.Contains(joined, "slot s3) has since been cleared") || strings.Contains(joined, "clear this task") {
		t.Fatalf("cleared declaration was presented as an old quote: %v", updates)
	}
	inbox, err = e.Inbox(ctx, worker)
	if err != nil {
		t.Fatal(err)
	}
	updates, _ = inbox["agent_updates"].([]string)
	if strings.Contains(strings.Join(updates, "\n"), fullText) {
		t.Fatalf("restart notice read twice: %v", updates)
	}
	_, err = e.query(ctx, func() core.Result {
		before := e.snapshotAppRestart(time.Now().UTC())
		_, _, observeErr := e.observeAppRestart("app:2", "app:3", time.Now().UTC(), before)
		return core.Result{"error": observeErr}
	})
	if err != nil {
		t.Fatal(err)
	}
	checkIn, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: worker})
	if err != nil {
		t.Fatal(err)
	}
	checkUpdates, _ := checkIn["agent_updates"].([]string)
	if !strings.Contains(strings.Join(checkUpdates, "\n"), "post-restart change") {
		t.Fatalf("check_in omitted its authenticated restart notice: %v", checkIn)
	}
}

func TestAppRestartOffDoesNotRecordEpoch(t *testing.T) {
	e, ctx, _, _ := configureBoard(t)
	res, err := e.query(ctx, func() core.Result {
		plans, _, err := e.observeAppRestart("app:1", "app:2", time.Now(), nil)
		return core.Result{"plans": len(plans), "epoch": e.state.RestartEpoch, "error": err}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res["plans"] != 0 || res["epoch"] != "" {
		t.Fatalf("disabled restart changed state: %v", res)
	}
}

func TestAppEpochWatcherRecordsOneStableReplacement(t *testing.T) {
	e, ctx, _, worker := configureBoard(t)
	onLoop(t, ctx, e, func(st *core.State) { st.Agents["hand"].Role = core.RoleCoordinator })
	if _, err := e.Configure(ctx, worker, core.RestartResumeSetting, "1h"); err != nil {
		t.Fatal(err)
	}
	var watch appEpochWatch
	for _, epoch := range []string{"A", "A", "A", "B", "", "B", "B", "B"} {
		if err := watch.observe(ctx, e, epoch, epoch != ""); err != nil {
			t.Fatal(err)
		}
	}
	res, err := e.query(ctx, func() core.Result {
		return core.Result{"epoch": e.state.RestartEpoch, "serial": e.state.Serial}
	})
	if err != nil || res["epoch"] != "B" {
		t.Fatalf("stable replacement was not recorded: %v, %v", res, err)
	}
	baseline := res["serial"]
	if err := watch.observe(ctx, e, "B", true); err != nil {
		t.Fatal(err)
	}
	res, err = e.query(ctx, func() core.Result { return core.Result{"serial": e.state.Serial} })
	if err != nil || res["serial"] != baseline {
		t.Fatal("same app epoch emitted a second restart op")
	}
}

func TestRestartSelectionExcludesStaleBootGraceRemoteAndInvalidThread(t *testing.T) {
	e, ctx, _, worker := configureBoard(t)
	e.SetWakeCommands(map[string]WakeCommand{"codex": {
		Argv: []string{"codex", "queue", "--thread", "{thread}", "--message", "{message}"},
	}})
	onLoop(t, ctx, e, func(st *core.State) { st.Agents["hand"].Role = core.RoleCoordinator })
	if _, err := e.Configure(ctx, worker, core.RestartResumeSetting, "1h"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	res, err := e.query(ctx, func() core.Result {
		l := e.state.Agents["hand"]
		l.Agent = &core.AgentInfo{Harness: "codex", Surface: harnessenv.ChatGPTApp, HostID: e.HostID()}
		l.CurrentSession = "12345678-1234-1234-1234-123456789abc"
		l.LastCoordination = now.Add(-90 * time.Minute)
		e.seen[l.ID] = time.Time{}
		stale := len(e.snapshotAppRestart(now))
		e.seen[l.ID] = now
		e.bootGrace = map[string]bootEvidence{}
		e.bootGrace[l.ID] = bootEvidence{at: now, created: l.CreatedSerial}
		boot := len(e.snapshotAppRestart(now))
		delete(e.bootGrace, l.ID)
		l.LastCoordination = now.Add(-30 * time.Minute)
		e.seen[l.ID] = time.Time{}
		recent := len(e.snapshotAppRestart(now))
		l.Agent.HostID = "some-other-host"
		remote := len(e.snapshotAppRestart(now))
		l.Agent.HostID = e.HostID()
		l.CurrentSession = "not-a-thread"
		invalid := len(e.snapshotAppRestart(now))
		l.CurrentSession = "12345678-1234-1234-1234-123456789abc"
		e.wakers.mu.Lock()
		e.wakers.queued = map[string]time.Time{l.ID: now}
		e.wakers.mu.Unlock()
		queued := e.snapshotAppRestart(now)
		coalesced := len(queued) == 1 && len(queued[0].plan.argv) == 0
		return core.Result{
			"stale": stale, "boot": boot, "recent": recent,
			"remote": remote, "invalid": invalid, "coalesced": coalesced,
		}
	})
	if err != nil || res["stale"] != 0 || res["boot"] != 0 ||
		res["recent"] != 1 || res["remote"] != 0 || res["invalid"] != 0 || res["coalesced"] != true {
		t.Fatalf("selection boundary: %v, %v", res, err)
	}
}
