// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/ledger"
)

// A real encrypted, uncompacted ledger is first readable by an independently
// built old daemon, then refused after the current writer boots. -check is
// read-only; no listener or real notification is started by the old process.
func TestReaderFenceOnRealUncompactedRecoveryLedger(t *testing.T) {
	for _, approved := range []bool{false, true} {
		name := "pending"
		if approved {
			name = "approved"
		}
		t.Run(name, func(t *testing.T) { testReaderFenceOnRecoveryLedger(t, approved) })
	}
}

func testReaderFenceOnRecoveryLedger(t *testing.T, approved bool) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"node_id": "contact-restart", "local.secret": strings.Repeat("a", 64)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.jsonl")
	disk, err := ledger.Open(path, "contact-restart", box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	st := historicalRecoveryState(t, func(serial uint64, now time.Time, op *core.Op) {
		if err := disk.Append(serial, now, op); err != nil {
			t.Fatal(err)
		}
	})
	consent := st.Serial
	if approved {
		op := &core.Op{
			Kind: core.OpRespond, Token: "lead-token", MsgSerial: consent, Disposition: "approve",
			RecoveryActor: &core.MergeActor{ID: "lead", Created: 3, Authority: "coordinator", Evidence: core.MergeEvidence{Known: true}},
		}
		now := time.Now()
		if _, _, err := st.Apply(op, now); err != nil {
			t.Fatal("historical approval:", err)
		}
		if err := disk.Append(st.Serial, now, op); err != nil {
			t.Fatal(err)
		}
		if len(st.RecoveredAddresses) == 0 || len(st.Agents["survivor"].InheritedDeclarations) != 1 {
			t.Fatal("setup: historical approval did not recover addresses and declaration")
		}
	}
	old := os.Getenv("DIBS_TEST_FEATURE_OLD_DAEMON")
	if old != "" {
		assertOldFeatureReader(t, old, dir, path, false)
	}
	e := New(st, disk, deadProber{})
	e.SetHumanNotifier(noContactDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	t.Cleanup(func() { cancel(); <-e.Done() })
	var want []byte
	if _, err := e.query(ctx, func() core.Result {
		if len(e.state.Messages) != 1 || e.state.Messages[consent].Recovery == nil {
			t.Error("boot removed historical recovery consent")
		}
		var encodeErr error
		want, encodeErr = stateBytes(e.state)
		if encodeErr != nil {
			t.Error(encodeErr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-e.Done()
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ledger.SnapshotName)); !os.IsNotExist(err) {
		t.Fatalf("control is not an uncompacted ledger: %v", err)
	}
	if old != "" {
		assertOldFeatureReader(t, old, dir, path, true)
	}
	readOnly, err := ledger.OpenReadOnly(path, "contact-restart", box)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readOnly.Close() }()
	got := core.NewState("contact-restart", core.DefaultLimits())
	if _, err := readOnly.Replay(got); err != nil {
		t.Fatal("current reader rejected historical/fenced tail:", err)
	}
	actual, err := stateBytes(got)
	if err != nil || !bytes.Equal(want, actual) {
		t.Fatal("current writer and actual encrypted replay disagree:", err)
	}
}

func stateBytes(st *core.State) ([]byte, error) {
	// Compare the persisted fields without leaking fixture credentials in an
	// assertion. Gob has map-order variation, whereas encoding/json sorts maps.
	return json.Marshal(st)
}

func assertOldFeatureReader(t *testing.T, old, dir, path string, refuse bool) {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, old, "-dir", dir, "-check", "-addr", "127.0.0.1:4777")
	cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_NOTIFY=off")
	out, runErr := cmd.CombinedOutput()
	t.Logf("old reader fenced=%v exit-error=%v output=%s", refuse, runErr, out)
	if refuse {
		if runErr == nil || !strings.Contains(string(out), requiredV17Kind) {
			t.Fatalf("old reader did not refuse with the minimum reader version: %v %s", runErr, out)
		}
	} else if runErr != nil {
		t.Fatalf("old reader cannot read the unfenced control: %v %s", runErr, out)
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("old read-only preflight altered ledger bytes:", err)
	}
}
