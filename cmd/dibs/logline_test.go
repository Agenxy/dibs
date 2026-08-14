package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The op and the agent must never run together.
//
// The op column's width is a guess at the longest ledgered op name, and the guess
// has been wrong twice. At 18 it collided with activity_checkpoint. At 19 it
// padded that op to exactly zero characters and the agent was concatenated
// straight onto it ("activity_checkpointorchestrator") which is what a reviewer
// reported reading in `dibs log`.
//
// So this pins the property rather than the width: whatever the longest op grows
// to, there is a separator. A test that only checked the current constant would
// pass again the day somebody adds a longer op kind.
func TestTheOpNeverRunsIntoTheSpace(t *testing.T) {
	when := time.Date(2026, 8, 7, 11, 5, 36, 0, time.UTC)
	for _, op := range []string{
		"activity_checkpoint",        // the exact width of the column
		"a_very_long_future_op_kind", // longer than it
		"send",                       // comfortably shorter
	} {
		line := logLine(43, when, op, "orchestrator", "")
		if strings.Contains(line, op+"orchestrator") {
			t.Errorf("op %q runs straight into the agent: %q", op, line)
		}
		if !strings.Contains(line, "orchestrator") {
			t.Errorf("op %q lost the agent entirely: %q", op, line)
		}
	}
}

// Every ledger record must reach the reader, registrations included.
//
// `agent` carries two shapes: an agent id on most ops, and the DESCRIPTOR
// object (harness, model, cwd) on register. Typed as a string, every register
// line failed to unmarshal and the reader dropped it without a word: 100 ledger
// records rendered as 86 rows, with every registration among the missing.
//
// An agent joining is the event people come to the log to confirm. A peer
// reported exactly that and could corroborate it nowhere, which is how this was
// found.
func TestEveryLedgerRecordRendersIncludingRegistrations(t *testing.T) {
	for _, tc := range []struct {
		name, line, wantActor string
	}{
		{
			name:      "register carries the descriptor object",
			line:      `{"s":46,"t":"2026-08-13T20:30:59Z","e":"register","op":{"name":"codex-root","agent":{"harness":"Codex","cwd":"/tmp"}}}`,
			wantActor: "codex-root",
		},
		{
			name:      "an ordinary op carries an agent id",
			line:      `{"s":47,"t":"2026-08-13T20:31:05Z","e":"check_in","op":{"agent":"codex-root"}}`,
			wantActor: "codex-root",
		},
		{
			name:      "an op that names the id explicitly",
			line:      `{"s":48,"t":"2026-08-13T20:31:20Z","e":"prune","op":{"agent_id":"stale-1"}}`,
			wantActor: "stale-1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rec ledgerRow
			if err := json.Unmarshal([]byte(tc.line), &rec); err != nil {
				t.Fatalf("the row did not parse, so the reader would drop it silently: %v", err)
			}
			if got := rec.actor(); got != tc.wantActor {
				t.Errorf("actor = %q, want %q: the row renders with nobody in it", got, tc.wantActor)
			}
		})
	}
}

// The client must speak the scheme the daemon serves.
//
// Every request in this binary was built as "http://" + addr(), in eighteen
// places. That is right for loopback and wrong for everything else: the daemon
// serves plaintext on loopback and TLS on any other address, so moving a daemon
// off 127.0.0.1 to serve a second machine broke its own CLI against a daemon
// that was working perfectly.
//
// The rule is not duplicated policy, it is the SAME rule read from the same
// side: loopback is plaintext, anything reachable by another host is TLS.
func TestTheClientDerivesTheSchemeTheDaemonServes(t *testing.T) {
	for _, tc := range []struct {
		name, dibsAddr, want string
	}{
		{name: "default is loopback plaintext", dibsAddr: "", want: "http://127.0.0.1:4777"},
		{name: "explicit loopback", dibsAddr: "127.0.0.1:4777", want: "http://127.0.0.1:4777"},
		{name: "localhost by name", dibsAddr: "localhost:4777", want: "http://localhost:4777"},
		{name: "a LAN address gets TLS", dibsAddr: "192.168.1.205:4777", want: "https://192.168.1.205:4777"},
		{name: "any routable address gets TLS", dibsAddr: "10.0.0.9:4799", want: "https://10.0.0.9:4799"},
		{name: "a hostname is not assumed local", dibsAddr: "studio.internal:4777", want: "https://studio.internal:4777"},
		// The one case the rule cannot infer: a daemon deliberately serving
		// plaintext off-loopback. An explicit scheme wins.
		{name: "an explicit scheme wins", dibsAddr: "http://10.0.0.9:4777", want: "http://10.0.0.9:4777"},
		{name: "explicit https on loopback wins too", dibsAddr: "https://127.0.0.1:4777", want: "https://127.0.0.1:4777"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DIBS_ADDR", tc.dibsAddr)
			if got := origin(); got != tc.want {
				t.Errorf("origin() = %q, want %q", got, tc.want)
			}
		})
	}
}
