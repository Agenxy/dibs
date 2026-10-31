// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// A static property: every persisted field added after the published v0.0.16
// schema must be assigned to a declared reader feature. Conditional marker
// behavior is tested separately through the actual snapshot writer.
func TestEveryNewSnapshotFieldHasADeclaredFeature(t *testing.T) {
	names := map[string]bool{}
	gobFieldNames(reflect.TypeFor[core.State](), map[reflect.Type]bool{}, names)
	assigned := map[string]bool{}
	features := map[string]bool{}
	for _, feature := range snapshotFeatures {
		if feature.name == "" || feature.present == nil || len(feature.fields) == 0 || features[feature.name] {
			t.Fatalf("incomplete feature declaration: %q", feature.name)
		}
		features[feature.name] = true
		for _, field := range feature.fields {
			if !names[field] || assigned[field] {
				t.Fatalf("feature %s assigns a missing or duplicate field: %s", feature.name, field)
			}
			assigned[field] = true
			delete(names, field)
		}
	}
	// Exact fingerprint of the published v0.0.16 reachable State fields.
	// Never update this baseline for new fields: assign a feature above.
	if got := fingerprint(names); got != "sha256:a1d51d01f9dc23d0" {
		t.Fatalf("a persisted field has no declared feature, or a v0.0.16 field changed: %s", got)
	}
}

func TestSnapshotHeaderRefusesUnknownFeaturesBeforeDecoding(t *testing.T) {
	for _, header := range []SnapshotHeader{
		{Version: 3},
		{Version: 2, Features: []string{"future-feature/v1"}},
	} {
		dir := t.TempDir()
		box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, SnapshotName)
		raw, err := json.Marshal(header)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, []byte("\nnot encrypted state")...), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _, err = readSnapshot(path, box)
		if err == nil || !strings.Contains(err.Error(), "upgrade dibd") {
			t.Fatalf("unknown requirement did not refuse with its corrective: %+v %v", header, err)
		}
	}
}

func TestSnapshotFeatureMarkersThroughRealWriter(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*core.State)
		want []string
	}{
		{"untouched", func(*core.State) {}, nil},
		{"enabled", func(s *core.State) { s.DesktopApproval.Enabled = true }, []string{"desktop-approval-confirmation/v1"}},
		{"disabled-with-provenance", func(s *core.State) { s.DesktopApproval.By = "human"; s.DesktopApproval.At = t0 }, []string{"desktop-approval-confirmation/v1"}},
		{"recovered-address", func(s *core.State) {
			s.RecoveredAddresses = map[string]core.RecoveredAddress{"source": {Source: "source", Into: "survivor", Serial: 1}}
		}, []string{"mailbox-recovery/v1"}},
		{"inherited-declaration", func(s *core.State) {
			s.Agents["survivor"] = &core.Agent{ID: "survivor", InheritedDeclarations: map[string]core.InheritedDeclaration{"old": {Source: "source", Declaration: core.Slot{Text: "retained history"}}}}
		}, []string{"mailbox-recovery/v1"}},
		{"pending-recovery", func(s *core.State) {
			s.Messages[1] = &core.Message{Serial: 1, Type: core.MsgRequest, Recovery: &core.RecoveryRequest{From: "source", Into: "survivor"}}
		}, []string{"mailbox-recovery/v1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			box, err := LoadOrCreateKey(filepath.Join(dir, "key"))
			if err != nil {
				t.Fatal(err)
			}
			state := core.NewState("test", core.DefaultLimits())
			tc.set(state)
			path := filepath.Join(dir, SnapshotName)
			h := SnapshotHeader{Serial: state.Serial, Node: state.NodeID, Head: genesis, Created: t0}
			if err := writeSnapshot(path, h, state, box); err != nil {
				t.Fatal(err)
			}
			header, got, ok, err := readSnapshot(path, box)
			if err != nil || !ok || !sameState(reflect.ValueOf(state).Elem(), reflect.ValueOf(got).Elem()) {
				t.Fatalf("writer/reader lost state: ok=%v err=%v", ok, err)
			}
			wantVersion := 1
			if len(tc.want) > 0 {
				wantVersion = 2
			}
			if header.Version != wantVersion || !slices.Equal(header.Features, tc.want) {
				t.Fatalf("marker: %+v, want version %d features %v", header, wantVersion, tc.want)
			}
			old := os.Getenv("DIBS_TEST_APPROVAL_OLD_DAEMON")
			if old == "" || wantVersion == 1 {
				return
			}
			for name, data := range map[string]string{"node_id": "test", "local.secret": strings.Repeat("a", 64), "ledger.jsonl": ""} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// A metadata-only old-format control, retaining identical encrypted
			// gob bytes, reproduces pre-marker snapshots such as shipped #530.
			legacy := header
			legacy.Version, legacy.Features = 1, nil
			prefix, err := json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(append(prefix, '\n'), raw[bytes.IndexByte(raw, '\n')+1:]...), 0o600); err != nil {
				t.Fatal(err)
			}
			// New readers still accept these valid historical snapshots.
			if _, _, _, err := readSnapshot(path, box); err != nil {
				t.Fatalf("rejected a valid legacy snapshot: %v", err)
			}
			preflight := func() ([]byte, error) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, old, "-dir", dir, "-check", "-addr", "127.0.0.1:4777")
				cmd.Env = append(os.Environ(), "DIBS_DIR="+dir, "DIBS_NOTIFY=off")
				return cmd.CombinedOutput()
			}
			if output, err := preflight(); err != nil {
				t.Fatalf("old-format control was not readable by the actual old daemon: %v %s", err, output)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := preflight()
			if err == nil || !strings.Contains(string(output), "snapshot version 2") {
				t.Fatalf("new marker did not refuse the old reader for the intended reason: %v %s", err, output)
			}
			t.Logf("actual old binary: legacy control accepted; feature snapshot refused: %s", output)
		})
	}
}
