package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// The retired words must be words this build has actually retired.
//
// This shipped listing the vocabulary it had just been renamed TO: `register`,
// `check_in`, `declare`, `send`. The rename sweep rewrote the guard's own table
// along with every other occurrence, and nothing noticed, because the guard only
// ran after a replay failure and its fixtures were rewritten by the same sweep.
// The result was a daemon that, on any replay error, told a person their current
// board had been written by 0.0.2 and to move it aside.
//
// So this asks the core, rather than trusting a second hand-maintained list: a
// word is retired only if Apply no longer knows it.
//
// The probe carries a real token because Apply resolves the actor BEFORE it
// dispatches on kind: without one, every kind alive or dead dies at E_BAD_TOKEN
// and the test passes on all of them. The first version of this did exactly
// that, in a file whose own comment says so.
func TestTheRetiredWordsAreRetired(t *testing.T) {
	for kind := range retiredOpKinds {
		s := core.NewState("n1", core.DefaultLimits())
		if _, _, err := s.Apply(&core.Op{
			Kind: core.OpRegister, Name: "probe", NewToken: "tok-probe",
		}, t0); err != nil {
			t.Fatalf("setup: registering the probe failed: %v", err)
		}
		_, _, err := s.Apply(&core.Op{Kind: kind, Token: "tok-probe"}, t0)
		if err == nil || !strings.Contains(err.Error(), "E_BAD_OP") {
			t.Errorf("%q is listed as retired, but this build still folds it (err = %v).\n"+
				"A live word in this table makes the guard condemn healthy boards.", kind, err)
		}
	}
}

// The retired FIELD names must not be fields this build writes.
//
// The field half cannot ask the core the same way: an unknown field is not an
// error anywhere, which is the whole reason it needs a guard. So it asks the
// wire format directly. A tag on core.Op that also appears in retiredOpFields
// would mean the daemon refuses to boot on ledgers it wrote itself.
func TestNoRetiredFieldIsStillOnTheWire(t *testing.T) {
	for _, tag := range jsonTagsOf(reflect.TypeOf(core.Op{})) {
		if retiredOpFields[tag] {
			t.Errorf("core.Op still writes %q, which is listed as retired: "+
				"the daemon would refuse the ledgers it writes itself", tag)
		}
	}
}

// A ledger from v0.0.4 is caught, and this is the case that was missed.
//
// `lane_kind` replays without any error at all. The op applies, the field is
// silently zero, and a persistent agent folds back as ephemeral: no nonce
// resume, no coordinator eligibility, `state == fold(ledger)` broken with
// nothing raised. Found by an agent coming back ephemeral on a real board, not
// by any test, which is why this one exists.
func TestAnOlderLedgerIsCaughtEvenThoughItReplaysClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	// A v0.0.4 register: current op kind, retired field name. Nothing about it
	// fails, which is the point.
	write(t, path, `{"op":{"kind":"register","name":"boss","lane_kind":"persistent"}}`)

	if got := retiredVocabulary(path); got != "lane_kind" {
		t.Fatalf("retiredVocabulary = %q, want \"lane_kind\": an upgraded board "+
			"silently demotes every persistent agent to ephemeral", got)
	}
	if err := oldVocabularyFailure(dir); err == nil {
		t.Fatal("the daemon opened a board it will fold incorrectly")
	}
}

// A retired kind is caught wherever it sits, not only in the opening records.
//
// The scan stopped at 50 lines. A board of any age keeps its registrations at
// the top and its sweeps further down, so a bounded scan answers "clean" for a
// file it has not read: a false clean, which is the failure being prevented.
func TestARetiredWordIsFoundPastTheOpeningRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(`{"op":{"kind":"heartbeat"}}` + "\n")
	}
	b.WriteString(`{"op":{"kind":"sweep","dead_lanes":["a-1"]}}` + "\n")
	write(t, path, b.String())

	if got := retiredVocabulary(path); got != "dead_lanes" {
		t.Errorf("retiredVocabulary = %q, want \"dead_lanes\": a replayed sweep "+
			"that marks nobody dead resurrects every agent the probe buried", got)
	}
}

// A board this build wrote opens.
//
// The counterpart to the test above, and the one that would have failed on the
// shipped table: every op below is current, so the guard must find nothing.
func TestACurrentLedgerIsNotCondemned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	var b strings.Builder
	for _, op := range []core.Op{
		{Kind: core.OpRegister, Name: "boss", AgentKind: core.KindPersistent},
		{Kind: core.OpAckBoard},
		{Kind: core.OpSetSlot},
		{Kind: core.OpClearSlot},
		{Kind: core.OpSendMessage},
		{Kind: core.OpAckMessage},
		{Kind: core.OpSignOff},
		{Kind: core.OpWake},
		{Kind: core.OpPrune},
		{Kind: core.OpUpdate},
		{Kind: core.OpResume},
		{Kind: core.OpSweep},
	} {
		raw, err := json.Marshal(struct {
			Op core.Op `json:"op"`
		}{op})
		if err != nil {
			t.Fatalf("marshal %s: %v", op.Kind, err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	write(t, path, b.String())

	if got := retiredVocabulary(path); got != "" {
		t.Fatalf("a board written by THIS build was condemned as obsolete over %q; "+
			"the daemon would tell its owner to move a healthy ledger aside", got)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// jsonTagsOf returns the wire names a struct writes, walking embedded structs.
func jsonTagsOf(typ reflect.Type) []string {
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			out = append(out, jsonTagsOf(f.Type)...)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}
