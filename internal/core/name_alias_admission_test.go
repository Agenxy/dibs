package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Decode the additive wire field, so the old-code run enters old Admit too.
// Every rejected update must still fold: today's release rules cannot decide
// whether yesterday's already-accepted history is replayable.
func TestNameReleaseAdmissionDoesNotBindHistoricalFold(t *testing.T) {
	lim := DefaultLimits()
	for _, raw := range []string{
		`{"kind":"update","release_names":[""]}`,
		`{"kind":"update","release_names":["   "]}`,
		`{"kind":"update","release_names":["` + strings.Repeat("x", lim.MaxNameBytes+1) + `"]}`,
		`{"kind":"update","release_names":[` + strings.TrimSuffix(strings.Repeat(`"a",`, 65), ",") + `]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var op Op
			if err := json.Unmarshal([]byte(raw), &op); err != nil {
				t.Fatal("setup:", err)
			}
			if err := Admit(&op, lim); err == nil {
				t.Fatalf("release shape reached the live fold: %s", raw)
			}
			st := NewState("historical-alias", lim)
			now := time.Unix(1234, 0)
			if _, _, err := st.Apply(&Op{Kind: OpRegister, Name: "worker", NewToken: "fixture-token"}, now); err != nil {
				t.Fatal("setup:", err)
			}
			op.Token = "fixture-token"
			before := st.Serial
			if _, _, err := st.Apply(&op, now); err != nil || st.Serial != before+1 {
				t.Fatalf("historical update refused or changed its serial: %v", err)
			}
		})
	}
}
