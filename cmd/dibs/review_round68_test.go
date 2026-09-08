package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A same-nonce register can move a live agent to another thread WITHOUT
// rotating its token. The watcher kept the existing subscription on a token
// match, so the stream went on naming the thread the agent had left, the
// daemon withheld its inbox from a stream whose agent is elsewhere, and the
// connection stayed open waking nobody.
func TestAStreamIsReplacedWhenItsSessionMovesEvenWithTheSameToken(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	resetWakeStreams()
	t.Cleanup(resetWakeStreams)
	t.Cleanup(func() { noteThread("") })

	seen := make(chan string, 8)
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sid, _ := req.Params.Meta["com.dibs/session"].(string)
		seen <- sid
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-hold
	}))
	defer srv.Close()
	defer close(hold)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var iw inboxWatcher

	threadA := "019ffe52-0eaf-7f60-81cc-6ab1298d76ec"
	threadB := "019ffe52-0eaf-7f60-81cc-6ab1298d76ed"

	noteThread(threadA)
	iw.startFor(ctx, srv.Client(), srv.URL, "secret", "busy", "same-token", 0)
	if got := awaitString(t, seen, 2*time.Second); got != threadA {
		t.Fatalf("setup: the first stream serves %q, want thread A", got)
	}

	// The agent moves to thread B, keeping its token.
	noteThread(threadB)
	iw.startFor(ctx, srv.Client(), srv.URL, "secret", "busy", "same-token", 0)
	got := awaitString(t, seen, 2*time.Second)
	if got == "" {
		t.Fatal("the agent moved to another thread and the watcher opened no new stream: the " +
			"old one names the thread it left, so the daemon withholds its inbox and this " +
			"bridge can never be woken again")
	}
	if got != threadB {
		t.Fatalf("the replacement stream serves %q, want thread B", got)
	}
}

func awaitString(t *testing.T, ch <-chan string, within time.Duration) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(within):
		return ""
	}
}

// Round sixty-seven made recovery read a spaced binary path whole and left the
// DRIFT check on the whitespace-excluding pattern, so upgrade called a correct
// unit wrong and reconcile would rewrite it, discarding operator tuning.
func TestACorrectUnitWithASpacedBinaryPathIsNotCalledDrifted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installed := filepath.Join(home, "Example User", "bin", "dibd")
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	board := filepath.Join(home, "Fleet Review", ".dibs")
	if err := os.MkdirAll(board, 0o755); err != nil {
		t.Fatal(err)
	}
	unitDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(unitDir, "org.agenxy.dibs.plist")
	body := "<plist><dict><key>ProgramArguments</key><array>" +
		"<string>" + installed + "</string><string>-dir</string><string>" + board + "</string>" +
		"</array></dict></plist>"
	if err := os.WriteFile(unit, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// The systemd shape of the same thing: its directive is ONE token,
	// `ExecStart=/path/to/dibd`, so it needs the key stripped before the value
	// is a path at all. Neither existing test covered a space AND systemd.
	{
		sd := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(sd, ".config"))
		unitDir := filepath.Join(sd, ".config", "systemd", "user")
		if err := os.MkdirAll(unitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		sdUnit := filepath.Join(unitDir, "dibs.service")
		line := "[Service]\nExecStart=" + systemdArg(installed) + " -dir " + systemdArg(board) + "\n"
		if err := os.WriteFile(sdUnit, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := unitBinary(sdUnit); got != installed {
			t.Fatalf("a systemd unit's spaced binary path read as %q, want %q", got, installed)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	gotUnit, pinned := unitDaemon()
	if gotUnit != unit {
		t.Fatalf("unitDaemon found %q, want %q", gotUnit, unit)
	}
	if pinned != installed {
		t.Fatalf("unitDaemon read the pinned binary as %q, want the whole path %q: the drift "+
			"check would call this correct unit wrong and rewrite it", pinned, installed)
	}
	if !sameBinary(pinned, installed) {
		t.Fatal("a correct unit reads as pinning a different binary, so upgrade would rewrite " +
			"it and discard whatever the operator tuned in it")
	}
}

// A notice deferred to the end of the cooldown carried nothing but its text,
// so retiring the stream that armed it left the timer running: an agent that
// moved during the cooldown still had its former session interrupted, past
// every standing check the daemon makes on the way in.
func TestADeferredNoticeIsDroppedWhenItsSubscriptionIsRetired(t *testing.T) {
	sock := sockPath(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", sock)
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "child-token")
	t.Cleanup(func() { recordWakePending(false) })
	lines := listenLines(t, sock)

	var iw inboxWatcher
	iw.cooldown = 400 * time.Millisecond
	w := iw.sharedWaker()
	if w == nil {
		t.Fatal("setup: no waker")
	}
	st := &inboxStream{key: "busy", token: "tok", session: "host-1"}
	iw.mu.Lock()
	iw.streams = map[string]*inboxStream{"busy": st}
	iw.mu.Unlock()

	// The first notice lands and spends the cooldown.
	if err := w.wakeWhile(selfWakeNotice, st.key, func() bool { return iw.streamIsCurrent(st) }); err != nil {
		t.Fatal("setup:", err)
	}
	if got := collect(lines, 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("setup: the first notice put %d line(s) into the session, want 2", len(got))
	}
	// A second arrival inside the cooldown is deferred.
	if err := w.wakeWhile(selfWakeNotice, st.key, func() bool { return iw.streamIsCurrent(st) }); err != nil {
		t.Fatal("setup:", err)
	}
	// Before it fires, the stream is retired: the agent moved on.
	iw.mu.Lock()
	iw.streams["busy"] = &inboxStream{key: "busy", token: "tok", session: "host-2"}
	iw.mu.Unlock()

	if got := collect(lines, 2, 2*time.Second); len(got) != 0 {
		t.Errorf("the deferred notice interrupted this session after its subscription was "+
			"retired: %d line(s) arrived for a session the agent has left", len(got))
	}
}
