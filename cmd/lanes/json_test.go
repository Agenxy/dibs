package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout runs fn with stdout redirected into a buffer, so a test can
// hold the document a --json path emitted. The commands under test print with
// fmt to os.Stdout, exactly like their human paths: capturing is the test's
// job, not a seam the production code should grow for it.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fnErr := fn()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = old
	return string(out), fnErr
}

// The JSON board must carry the facts the prose board renders, under the wire
// names, and nothing decorative. The fixture is the daemon's own payload
// shape (internal/core.State.Board plus the engine's presentation fields), so
// this also pins the CLI's field tags to the names the daemon actually sends:
// a tag that drifts from the wire decodes to a zero value here and fails
// loudly.
func TestBoardJSONCarriesWhatTheProseBoardShows(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/board" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{
			"serial": 42, "node": "n1",
			"lanes": [{
				"id": "codex-1", "name": "codex-1", "description": "refactors",
				"status": "stale", "stale_reason": "process_exited",
				"proc_alive": false, "last_seen": "2026-08-11T10:00:00Z",
				"slots": [{"id": "s1", "text": "fixing reconnects", "dirs": ["/tmp/x"]}]
			}],
			"claims": [{"lane": "codex-1", "path": "/tmp/x", "mode": "exclusive",
				"note": "migration", "renewed": "2026-08-11T10:00:00Z"}],
			"channels": [{"id": "reconnects", "topic": "session reconnects",
				"members": [{"agent": "codex-1", "auto": true, "score": 0.91}],
				"unacked_announcements": 2, "abandoned_announcements": 1,
				"blocked_announcements": 0, "departed_unacked": 0}]
		}`)
	}))
	defer daemon.Close()
	t.Setenv("LANES_DIR", t.TempDir())
	t.Setenv("LANES_ADDR", strings.TrimPrefix(daemon.URL, "http://"))

	out, err := captureStdout(t, func() error { return board([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Serial uint64 `json:"serial"`
		Node   string `json:"node"`
		Lanes  []struct {
			ID          string `json:"id"`
			Status      string `json:"status"`
			StaleReason string `json:"stale_reason"`
			LastSeen    string `json:"last_seen"`
			Slots       []struct {
				Text string   `json:"text"`
				Dirs []string `json:"dirs"`
			} `json:"slots"`
		} `json:"lanes"`
		Claims []struct {
			Lane string `json:"lane"`
			Path string `json:"path"`
			Mode string `json:"mode"`
		} `json:"claims"`
		Channels []struct {
			ID      string `json:"id"`
			Unacked int    `json:"unacked_announcements"`
		} `json:"channels"`
	}
	if uerr := json.Unmarshal([]byte(out), &doc); uerr != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", uerr, out)
	}
	if doc.Serial != 42 || doc.Node != "n1" {
		t.Errorf("serial/node lost in transit: %+v", doc)
	}
	if len(doc.Lanes) != 1 || doc.Lanes[0].ID != "codex-1" ||
		doc.Lanes[0].Status != "stale" || doc.Lanes[0].StaleReason != "process_exited" ||
		doc.Lanes[0].LastSeen == "" {
		t.Errorf("the lane facts the prose board shows are missing: %+v", doc.Lanes)
	}
	if len(doc.Lanes[0].Slots) != 1 || doc.Lanes[0].Slots[0].Text != "fixing reconnects" {
		t.Errorf("declared work is missing: %+v", doc.Lanes[0].Slots)
	}
	if len(doc.Claims) != 1 || doc.Claims[0].Mode != "exclusive" || doc.Claims[0].Path != "/tmp/x" {
		t.Errorf("claims are missing: %+v", doc.Claims)
	}
	if len(doc.Channels) != 1 || doc.Channels[0].Unacked != 2 {
		t.Errorf("channel counts are missing: %+v", doc.Channels)
	}
}

// An empty board is a normal state a script iterates over, so the lists have
// to be lists: null decodes fine in Go and trips over the first `for lane in
// doc["lanes"]` anywhere else.
func TestBoardJSONPinsEmptyListsToEmptyLists(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"serial": 0, "node": "n1", "lanes": []}`)
	}))
	defer daemon.Close()
	t.Setenv("LANES_DIR", t.TempDir())
	t.Setenv("LANES_ADDR", strings.TrimPrefix(daemon.URL, "http://"))

	out, err := captureStdout(t, func() error { return board([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"lanes":[]`, `"claims":[]`, `"channels":[]`} {
		if !strings.Contains(out, key) {
			t.Errorf("document lacks %s: a script iterating it meets null instead\n%s", key, out)
		}
	}
}

// writeLedger puts a small parseable ledger where `lanes log` will look.
func writeLedger(t *testing.T, records int) {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= records; i++ {
		fmt.Fprintf(&b, `{"s":%d,"t":"2026-08-11T0%d:00:00Z","e":"register_lane","op":{"lane":"codex-%d","to":""}}`+"\n",
			i, i, i)
	}
	if err := os.WriteFile(filepath.Join(dir, "ledger.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LANES_DIR", dir)
	t.Setenv("LANES_ADDR", "")
}

// The log stream must be one object per line, each carrying the same facts as
// the prose columns: serial, time, op, lane. The names are the document's
// own, not the ledger's single-letter storage keys.
func TestLogJSONEmitsOneParseableObjectPerEvent(t *testing.T) {
	writeLedger(t, 3)
	out, err := captureStdout(t, func() error { return logCmd([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 objects, got %d:\n%s", len(lines), out)
	}
	for i, line := range lines {
		var rec struct {
			Serial uint64 `json:"serial"`
			Time   string `json:"time"`
			Op     string `json:"op"`
			Lane   string `json:"lane"`
		}
		if uerr := json.Unmarshal([]byte(line), &rec); uerr != nil {
			t.Fatalf("line %d is not JSON: %v\n%s", i+1, uerr, line)
		}
		if rec.Serial != uint64(i+1) || rec.Op != "register_lane" ||
			rec.Lane != fmt.Sprintf("codex-%d", i+1) || rec.Time == "" {
			t.Errorf("line %d lost facts the prose shows: %+v", i+1, rec)
		}
	}
}

// The tail notice is decoration, and decoration must not reach stdout when a
// parser is attached: --limit under --json still trims, still says so, but
// says it on stderr.
func TestLogJSONKeepsTheTrimNoticeOffStdout(t *testing.T) {
	writeLedger(t, 5)
	out, err := captureStdout(t, func() error { return logCmd([]string{"--json", "--limit", "2"}) })
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want exactly the 2 trimmed objects on stdout, got %d:\n%s", len(lines), out)
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("non-JSON reached stdout under --json: %q", line)
		}
	}
}

// verify --json must agree with the prose verdicts on the same three ledgers
// the prose tests pin: intact, torn (NOT a failure: see chainResult), and
// damaged, where the error must still name both records so the reader knows
// where to look.
func TestVerifyJSONCarriesTheChainVerdict(t *testing.T) {
	parse := func(t *testing.T, out string) (rep struct {
		OK    bool   `json:"ok"`
		Path  string `json:"path"`
		Lines int    `json:"lines"`
		Head  string `json:"head"`
		Torn  bool   `json:"torn"`
		Error string `json:"error"`
	}) {
		t.Helper()
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
		}
		return rep
	}

	t.Run("intact", func(t *testing.T) {
		path, _ := chain(t, 5)
		out, err := captureStdout(t, func() error { return verify([]string{"--json", path}) })
		if err != nil {
			t.Fatal(err)
		}
		rep := parse(t, out)
		if !rep.OK || rep.Lines != 5 || rep.Head == "" || rep.Torn || rep.Path != path {
			t.Errorf("an intact chain must read ok with its head and line count: %+v", rep)
		}
	})

	t.Run("torn tail", func(t *testing.T) {
		path, lines := chain(t, 5)
		torn := lines[4][:len(lines[4])/2]
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"+torn), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := captureStdout(t, func() error { return verify([]string{"--json", path}) })
		if err != nil {
			t.Fatalf("a torn tail is not damage and must not fail: %v", err)
		}
		rep := parse(t, out)
		if !rep.OK || !rep.Torn || rep.Lines != 5 {
			t.Errorf("torn must be said, ok must hold: %+v", rep)
		}
	})

	t.Run("damaged", func(t *testing.T) {
		path, lines := chain(t, 5)
		lines[2] = strings.Replace(lines[2], `"e":"noop"`, `"e":"noOp"`, 1)
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := captureStdout(t, func() error { return verify([]string{"--json", path}) })
		if err == nil {
			t.Fatal("a broken chain must still fail the command, or monitoring sleeps through it")
		}
		var exitOnly interface{ exitOnly() }
		if !errors.As(err, &exitOnly) {
			t.Errorf("the failure is already in the document; stderr would repeat it: %v", err)
		}
		rep := parse(t, out)
		if rep.OK || rep.Error == "" {
			t.Errorf("the document must carry the verdict and the reason: %+v", rep)
		}
		for _, want := range []string{"serial 3", "serial 4"} {
			if !strings.Contains(rep.Error, want) {
				t.Errorf("the error must name %q, as the prose does: %s", want, rep.Error)
			}
		}
	})
}

// doctor --json must emit one document whatever the run finds, including the
// early aborts, and the exit status must keep agreeing with the problem tier
// the way TestDoctorFailsWhenTheInstallCannotBeChecked pins for the prose.
func TestDoctorJSONReportsTheEarlyAbortAsADocument(t *testing.T) {
	t.Setenv("LANES_DIR", t.TempDir())
	t.Setenv("LANES_ADDR", "127.0.0.1:1")
	out, err := captureStdout(t, func() error { return doctor([]string{"--json"}) })
	if err == nil {
		t.Fatal("doctor reported a missing local secret but returned success")
	}
	var doc doctorReport
	if uerr := json.Unmarshal([]byte(out), &doc); uerr != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", uerr, out)
	}
	if doc.Healthy || doc.Problems == 0 {
		t.Errorf("a missing secret is a problem and the document must say so: %+v", doc)
	}
	if len(doc.Checks) == 0 {
		t.Fatal("the document carries no checks: the diagnosis went to a person who is not there")
	}
	found := false
	for _, c := range doc.Checks {
		if c.Level == "problem" {
			found = true
			if c.Fix == "" {
				t.Errorf("a problem without its fix leaves the reader exactly as stuck: %+v", c)
			}
		}
	}
	if !found {
		t.Errorf("no check carries level \"problem\": %+v", doc.Checks)
	}
}
