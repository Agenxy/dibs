// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type staleQueueRow struct {
	ID    string
	Input []struct{ Type, Text string }
}

func staleQueueFixture(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "codex")
	if b, err := exec.Command("go", "build", "-o", binary, "./testdata/codexqueue").CombinedOutput(); err != nil {
		t.Fatalf("setup: fixture build: %v: %s", err, b)
	}
	return binary
}

func staleQueueHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_DIR", t.TempDir())
	return home
}

func staleQueueRows(t *testing.T, home string) []staleQueueRow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "pending.json"))
	if err != nil {
		t.Fatalf("setup: read actual queue: %v", err)
	}
	var rows []staleQueueRow
	if err = json.Unmarshal(b, &rows); err != nil {
		t.Fatalf("setup: decode actual queue: %v", err)
	}
	for _, row := range rows {
		if row.ID == "" || len(row.Input) != 1 || row.Input[0].Type != "text" {
			t.Fatalf("setup: fixture did not retain one text input: %#v", row)
		}
	}
	return rows
}

func assertIssuedQueueNotice(t *testing.T, text, kind string, started, finished time.Time) {
	t.Helper()
	if kind == "" || kind == "notice" {
		kind = "coordination"
	}
	prefix := "Dibs: " + kind + " notice issued at "
	const suffix = ". It may already be handled."
	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, suffix) {
		t.Fatalf("queued notice still claims current unread mail or lacks its issuance time: %q", text)
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(text, prefix), suffix)
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil || stamp != at.UTC().Truncate(time.Second).Format(time.RFC3339) || at.Before(started.Truncate(time.Second)) || at.After(finished) {
		t.Fatalf("notice has no valid UTC admission time within the real command call: %q, %v", stamp, err)
	}
}

func TestNativeQueueNoticeRecordsAdmissionAndKeepsOriginalTimeAcrossWriters(t *testing.T) {
	if os.Getenv("DIBS_STALE_NOTICE_CHILD") == "1" {
		argv := []string{os.Getenv("DIBS_STALE_NOTICE_BINARY"), "queue", "--thread", "stale-thread", "--message", Compose("handoff")}
		if !RunCommands(argv, nil, "other-writer", "", time.Second, time.Second) {
			t.Fatal("setup: independent command writer failed")
		}
		return
	}
	binary := staleQueueFixture(t)
	home := staleQueueHome(t)
	argv := []string{binary, "queue", "--thread", "stale-thread", "--message", Compose("question")}
	started := time.Now().UTC()
	if !RunCommands(argv, nil, "name-must-not-leak", "", time.Second, time.Second) {
		t.Fatal("setup: actual queue command failed")
	}
	finished := time.Now().UTC()
	rows := staleQueueRows(t, home)
	if len(rows) != 1 {
		t.Fatalf("actual admission queued %d items, want one", len(rows))
	}
	original := rows[0].Input[0].Text
	assertIssuedQueueNotice(t, original, "question", started, finished)
	if strings.Contains(original, "name-must-not-leak") || strings.Contains(original, "stale-thread") {
		t.Fatalf("participant identity escaped into queue text: %q", original)
	}
	if argv[5] != Compose("question") {
		t.Fatal("the command route rewrote its caller's argv")
	}
	// A prompt and an expired inference receipt cannot override a real item.
	NoteQueuePrompt("stale-thread", time.Now())
	if err := writeReceipt("stale-thread", queueReceipt{QueuedAt: time.Now().Add(-3 * time.Hour)}); err != nil {
		t.Fatal("setup: expire inference:", err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestNativeQueueNoticeRecordsAdmissionAndKeepsOriginalTimeAcrossWriters$")
	child.Env = append(os.Environ(), "DIBS_STALE_NOTICE_CHILD=1", "DIBS_STALE_NOTICE_BINARY="+binary)
	if b, err := child.CombinedOutput(); err != nil {
		t.Fatalf("independent writer: %v: %s", err, b)
	}
	rows = staleQueueRows(t, home)
	if len(rows) != 1 || rows[0].Input[0].Text != original {
		t.Fatalf("later mail changed the pending notice's original time or duplicated it: %#v", rows)
	}
}

func seedStaleQueue(t *testing.T, binary, text string) {
	t.Helper()
	// Simulate an app item written before this process/image existed; the
	// stand-in harness receives it through its actual CLI transaction.
	if b, err := exec.Command(binary, "queue", "--thread", "stale-thread", "--message", text).CombinedOutput(); err != nil {
		t.Fatalf("setup: app queue transaction: %v: %s", err, b)
	}
}

func TestNativeQueueRecognizesDatedPendingItemsThroughCommandDoor(t *testing.T) {
	binary := staleQueueFixture(t)
	for _, kind := range []string{"coordination", "notify", "question", "request", "handoff", "answered", "approved", "done", "adopted"} {
		t.Run(kind, func(t *testing.T) {
			home := staleQueueHome(t)
			text := fmt.Sprintf("Dibs: %s notice issued at 2026-01-01T00:00:00Z. It may already be handled.", kind)
			seedStaleQueue(t, binary, text)
			if rows := staleQueueRows(t, home); len(rows) != 1 || rows[0].Input[0].Text != text {
				t.Fatal("setup: historical dated item was not retained")
			}
			argv := []string{binary, "queue", "--thread", "stale-thread", "--message", Compose("handoff")}
			if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) {
				t.Fatal("setup: new writer's command route failed")
			}
			rows := staleQueueRows(t, home)
			if len(rows) != 1 || rows[0].Input[0].Text != text {
				t.Fatalf("new writer duplicated or changed the dated pending %s item: %#v", kind, rows)
			}
		})
	}
}

func TestNativeQueueHistoricalAndUnrecognizedItemsKeepDeliverySafe(t *testing.T) {
	binary := staleQueueFixture(t)
	cases := []struct {
		name, text string
		pending    bool
	}{
		{"legacy question", Compose("question"), true},
		{"legacy verdict", Compose("answered"), true},
		{"unowned item", "ordinary operator input", false},
		{"unknown kind", "Dibs: future-kind notice issued at 2026-01-01T00:00:00Z. It may already be handled.", false},
		{"invalid date", "Dibs: question notice issued at nonsense. It may already be handled.", false},
		{"noncanonical offset", "Dibs: question notice issued at 2026-01-01T00:00:00+00:00. It may already be handled.", false},
		{"fractional second", "Dibs: question notice issued at 2026-01-01T00:00:00.123Z. It may already be handled.", false},
		{"duplicated notice kind", "Dibs: notice notice issued at 2026-01-01T00:00:00Z. It may already be handled.", false},
		{"extra text", "Dibs: question notice issued at 2026-01-01T00:00:00Z. It may already be handled. arbitrary", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := staleQueueHome(t)
			seedStaleQueue(t, binary, c.text)
			started := time.Now().UTC()
			argv := []string{binary, "queue", "--thread", "stale-thread", "--message", Compose("question")}
			if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) {
				t.Fatal("setup: actual command route failed")
			}
			finished := time.Now().UTC()
			rows := staleQueueRows(t, home)
			want := 2
			if c.pending {
				want = 1
			}
			if len(rows) != want || rows[0].Input[0].Text != c.text {
				t.Fatalf("unrecognised text suppressed a wake or a legacy wake duplicated: %#v", rows)
			}
			if !c.pending {
				assertIssuedQueueNotice(t, rows[1].Input[0].Text, "question", started, finished)
			}
		})
	}
	t.Run("unavailable observation still issues a dated wake", func(t *testing.T) {
		home := staleQueueHome(t)
		t.Setenv("DIBS_QUEUE_PROBE_FAIL", "1")
		started := time.Now().UTC()
		argv := []string{binary, "queue", "--thread", "stale-thread", "--message", Compose("request")}
		if !RunCommands(argv, nil, "worker", "", time.Second, time.Second) {
			t.Fatal("setup: unknown observation stranded the wake")
		}
		finished := time.Now().UTC()
		rows := staleQueueRows(t, home)
		if len(rows) != 1 {
			t.Fatalf("unknown observation admitted %d items, want one", len(rows))
		}
		assertIssuedQueueNotice(t, rows[0].Input[0].Text, "request", started, finished)
	})
	t.Run("queue fallback shares dated admission", func(t *testing.T) {
		home := staleQueueHome(t)
		started := time.Now().UTC()
		primary := []string{binary, "primary"}
		fallback := []string{binary, "queue", "--thread", "stale-thread", "--message", Compose("handoff")}
		if !RunCommands(primary, fallback, "worker", "", time.Second, time.Second) {
			t.Fatal("setup: loaded-writer refusal did not enter queue fallback")
		}
		finished := time.Now().UTC()
		rows := staleQueueRows(t, home)
		if len(rows) != 1 {
			t.Fatalf("fallback admission queued %d items, want one", len(rows))
		}
		assertIssuedQueueNotice(t, rows[0].Input[0].Text, "handoff", started, finished)
	})
	t.Run("ordinary command is unchanged", func(t *testing.T) {
		home := staleQueueHome(t)
		plain := filepath.Join(t.TempDir(), "operator")
		image, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal("setup: read fixture:", err)
		}
		if err = os.WriteFile(plain, image, 0o700); err != nil {
			t.Fatal("setup: copy ordinary operator command:", err)
		}
		text := Compose("question")
		if !RunCommands([]string{plain, "queue", "--thread", "stale-thread", "--message", text}, nil, "worker", "", time.Second, time.Second) {
			t.Fatal("setup: ordinary command failed")
		}
		rows := staleQueueRows(t, home)
		if len(rows) != 1 || rows[0].Input[0].Text != text {
			t.Fatalf("the native-queue repair changed an ordinary operator command: %#v", rows)
		}
	})
}
