package engine

import (
	"context"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func TestBoardDistinguishesBootGraceFromAuthenticatedContact(t *testing.T) {
	st := core.NewState("seen", core.DefaultLimits())
	checkpoint := time.Now().Add(-time.Minute)
	if _, _, err := st.Apply(&core.Op{Kind: core.OpRegister, Name: "worker", NewToken: "token"}, checkpoint); err != nil {
		t.Fatal("replay fixture setup:", err)
	}
	e := New(st, &memLedger{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	row := func() map[string]any {
		t.Helper()
		board, err := e.Board(ctx) // production passive snapshot, no contact stamp
		if err != nil {
			t.Fatal(err)
		}
		for _, agent := range board["agents"].([]map[string]any) {
			if agent["id"] == "worker" {
				return agent
			}
		}
		t.Fatal("setup: worker missing")
		return nil
	}
	boot := row()
	if !boot["last_seen"].(time.Time).After(checkpoint) {
		t.Fatal("setup: production boot did not grant grace")
	}
	if boot["seen_source"] != "boot_grace" {
		t.Error("boot grace masqueraded as actual contact:", boot["seen_source"])
	}
	if _, err := e.Do(ctx, &core.Op{Kind: core.OpAckBoard, Token: "token"}); err != nil {
		t.Fatal("actual check_in:", err)
	}
	contact := row()
	if contact["seen_source"] != "authenticated_contact" {
		t.Error("authenticated check_in did not replace boot provenance:", contact["seen_source"])
	}
}

// These are decision inputs, not substitutes for the production boot/call
// regression above. Each case preserves the exact existing liveness timestamp.
func TestEvidenceProvenancePreservesClockAndIncarnation(t *testing.T) {
	checkpoint := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	runtime := checkpoint.Add(time.Minute)
	hook := runtime.Add(time.Minute)
	for _, tc := range []struct {
		name    string
		seen    time.Time
		hook    time.Time
		grace   uint64
		contact bool
		want    time.Time
		source  string
	}{
		{name: "durable-only", want: checkpoint, source: "ledger_activity"},
		{name: "runtime", seen: runtime, want: runtime, source: "activity"},
		{name: "boot", seen: runtime, grace: 2, want: runtime, source: "boot_grace"},
		{name: "older-incarnation", seen: runtime, grace: 1, want: runtime, source: "activity"},
		{name: "newer-hook", seen: runtime, hook: hook, grace: 2, want: hook, source: "harness_hook"},
		{
			name: "contact-ties-checkpoint", seen: checkpoint.In(time.FixedZone("other", 3600)), contact: true,
			want: checkpoint, source: "authenticated_contact",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{
				seen: map[string]time.Time{}, hookAlive: map[string]time.Time{},
				bootGrace: map[string]bootEvidence{}, contact: map[string]contactEvidence{},
			}
			l := &core.Agent{ID: "worker", CreatedSerial: 2, LastCoordination: checkpoint}
			if !tc.seen.IsZero() {
				e.seen[l.ID] = tc.seen
			}
			if !tc.hook.IsZero() {
				e.hookAlive[l.ID] = tc.hook
			}
			if tc.grace != 0 {
				e.bootGrace[l.ID] = bootEvidence{at: tc.seen, created: tc.grace}
			}
			if tc.contact {
				e.contact[l.ID] = contactEvidence{at: tc.seen, created: l.CreatedSerial}
			}
			at, source := e.lastEvidenceWithSource(l)
			if at != tc.want || e.lastEvidenceOf(l) != tc.want || source != tc.source {
				t.Fatalf("got %v/%s, want exact %v/%s", at, source, tc.want, tc.source)
			}
		})
	}
}
