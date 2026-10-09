// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/peerwake"
)

// The real child bridge registers and subscribes to the real daemon. Its
// actual ENOENT must hand the ORIGINAL mail to the daemon, without another
// inbox frame, stream disconnect or turn hook to rescue it.
func TestGoneBridgeSocketReleasesItsClaimAndDaemonDeliversOriginalMail(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("peer discovery requires ps on this platform")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := filepath.Dir(sockPath(t))
	t.Setenv("XDG_RUNTIME_DIR", base)
	if err := os.MkdirAll(filepath.Join(base, "cc-socks"), 0o700); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(os.Getpid())
	sock := filepath.Join(base, "cc-socks", pid+".sock")
	const sid = "71f97290-0001-4000-8000-444444444444"
	const daemonToken = "123456789012345678901234567890ab"
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ps", "-o", "lstart=", "-p", pid)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	stamp, err := cmd.Output()
	if err != nil || len(bytes.TrimSpace(stamp)) == 0 {
		t.Fatalf("setup: real process start: %q %v", stamp, err)
	}
	key, _ := json.Marshal(map[string]any{"peerToken": daemonToken, "procStart": strings.TrimSpace(string(stamp))})
	if err := os.WriteFile(filepath.Join(dir, pid+"."+strings.Repeat("a", 64)+".key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	side, _ := json.Marshal(map[string]any{"sessionId": sid})
	if err := os.WriteFile(filepath.Join(dir, pid+".json"), side, 0o600); err != nil {
		t.Fatal(err)
	}
	var recovered <-chan string
	var failures, releases atomic.Int32
	wrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			var req struct {
				Method string
				Params struct {
					Meta map[string]any `json:"_meta"`
				}
			}
			if err := json.Unmarshal(b, &req); err != nil {
				t.Error(err)
				return
			}
			meta := req.Params.Meta
			if req.Method == "resources/read" && meta["com.dibs/self_wake_release"] != nil {
				releases.Add(1)
			}
			if req.Method == "resources/read" && meta["com.dibs/socket_offer_id"] != nil && meta["com.dibs/socket_written"] == false && failures.Add(1) == 1 {
				// sendUsing's settlement is after the actual failed kernel dial, before
				// surrender. Recover the same endpoint before claim release, without
				// adding any mail/lifecycle event that could mask the defect.
				recovered = listenLines(t, sock)
			}
			next.ServeHTTP(w, r)
		})
	}
	f := newEconomyFixtureAt(t, sid, sock, wrap)
	found, err := peerwake.Discover(home, peerwake.Alive)
	if err != nil || len(found) != 1 || found[0].Socket != sock {
		t.Fatalf("setup: daemon cannot discover real fixture process: %v %v", found, err)
	}
	f.idle(t)
	if err := os.Remove(sock); err != nil {
		t.Fatal("setup unlink:", err)
	}
	f.tool(t, "send", map[string]any{"token": f.sender, "to": "worker", "type": "question", "body": "original private question"})
	select {
	case written := <-f.settles:
		if written {
			t.Fatal("setup: vanished socket write unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("setup: bridge never attempted the real ENOENT write")
	}
	if recovered == nil {
		t.Fatal("setup: socket was not recovered at failed settlement")
	}
	got := collect(recovered, 2, 3*time.Second)
	if len(got) != 2 || !strings.Contains(got[0], daemonToken) || !strings.Contains(got[1], "question") {
		live, _ := f.eng.SelfWaking("worker")
		t.Fatalf("original mail stranded after ENOENT: claim=%v release calls=%d wire=%v", live, releases.Load(), got)
	}
	if live, _ := f.eng.SelfWaking("worker"); live || releases.Load() != 1 {
		t.Fatalf("claim not explicitly handed back: live=%v releases=%d", live, releases.Load())
	}
	if got := collect(recovered, 1, 250*time.Millisecond); len(got) != 0 {
		t.Fatal("both writers delivered", got)
	}
}
