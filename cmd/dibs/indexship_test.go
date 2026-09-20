package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The bridge ships an index only when the daemon has said it could not read
// the tree: a daemon that read it has a better index than a copy, and one
// that has not looked yet may still read it. Issue #19.
func TestTheBridgeShipsOnlyWhenTheDaemonCouldNotRead(t *testing.T) {
	root := "/Users/me/Desktop/proj"
	for _, tc := range []struct {
		name string
		st   matchStatusJSON
		want bool
	}{
		{"daemon has not looked", matchStatusJSON{Phase: "off"}, false},
		{"daemon is indexing it", matchStatusJSON{Phase: "indexing", Repo: root}, false},
		{"daemon read it", matchStatusJSON{Phase: "ready", Repo: root}, false},
		{"daemon could not read it", matchStatusJSON{Phase: "ready", Unreadable: []string{root}}, true},
		{"daemon could not read a subdirectory of it", matchStatusJSON{Unreadable: []string{root + "/internal/core"}}, true},
		{"daemon could not read some OTHER tree", matchStatusJSON{Unreadable: []string{"/Users/me/Documents/x"}}, false},
	} {
		if got := wantsIndex(tc.st, root); got != tc.want {
			t.Errorf("%s: wantsIndex = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := indexURL("http://127.0.0.1:4777/mcp"); got != "http://127.0.0.1:4777/api/index" {
		t.Errorf("indexURL = %q", got)
	}
}

// The schedule must outlast the daemon's Git deadline, or the fallback it
// exists for never fires for the case that motivated it.
func TestTheShipScheduleOutlastsTheDaemonsGitDeadline(t *testing.T) {
	var total time.Duration
	for _, d := range shipSchedule {
		total += d
	}
	if total <= daemonGitDeadline+30*time.Second {
		t.Fatalf("the ship schedule gives up after %v, before the daemon has finished "+
			"waiting on Git (%v) and said the tree is unreadable", total, daemonGitDeadline)
	}
}

// The fallback asks the daemon it registered with, at the address it
// registered through.
//
// shipWhenUnreadable was handed the resolved URL and used it for the
// shipment, and asked for the verdict through fetchMatchStatus, which built
// its own URL from origin(): the configured address, before Supgang
// resolved the hub's current one. After the hub moved, registration went to
// the new address and every verdict poll went to the old one, so the index
// never shipped and nothing said why. Found by the pre-release review,
// round three.
func TestTheFallbackAsksTheDaemonItRegisteredWith(t *testing.T) {
	asked := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- r.URL.Path
		if r.URL.Path == "/api/match-status" {
			_ = json.NewEncoder(w).Encode(matchStatusJSON{Unreadable: []string{"/not/a/checkout"}})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	prev := shipSchedule
	shipSchedule = []time.Duration{time.Millisecond}
	t.Cleanup(func() { shipSchedule = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shipWhenUnreadable(ctx, srv.Client(), srv.URL+"/mcp", "secret", "token", "/not/a/checkout")

	select {
	case p := <-asked:
		if p != "/api/match-status" {
			t.Fatalf("first request to the resolved daemon was %q, want the verdict poll", p)
		}
	default:
		t.Fatal("the verdict was never asked of the daemon the bridge registered with: " +
			"the poll went to the configured address instead")
	}
}

// A tree the daemon reports as being on another machine is one to ship for:
// the daemon cannot read it by definition and never records an unreadable
// verdict for it. Round four of the pre-release review.
func TestTheBridgeShipsForATreeTheDaemonReportsRemote(t *testing.T) {
	root := "/srv/checkout"
	if !wantsIndex(matchStatusJSON{Phase: "ready", Remote: []string{root}}, root) {
		t.Error("a tree listed remote was not shipped for")
	}
	if wantsIndex(matchStatusJSON{Phase: "ready", Remote: []string{"/srv/other"}}, root) {
		t.Error("some OTHER remote tree triggered a shipment")
	}
}

// The bridge ships again after the daemon it shipped to has restarted.
//
// A supplied index lives in the daemon's memory; `dibs upgrade` or a reboot
// loses it, while the bridge and its token go on working. The shipment
// schedule had run out on registration and nothing asked again, so matching
// stayed unavailable for that tree until some other registration happened to
// ship. The bridge now keeps looking at the verdict, slowly, for as long as
// it lives, and ships whenever the daemon wants an index nobody has supplied.
// Round five of the pre-release review.
func TestTheBridgeShipsAgainAfterTheDaemonRestarts(t *testing.T) {
	// A real checkout, since a shipment mines it; resolved, because git
	// reports the real path and macOS's temp dir is a symlink.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup: git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "--no-gpg-sign", "-m", "a")

	var mu sync.Mutex
	shipped := 0
	restarted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/match-status":
			st := matchStatusJSON{Unreadable: []string{root}}
			if shipped > 0 && !restarted {
				st.Supplied = map[string]string{root: "agent"}
			}
			_ = json.NewEncoder(w).Encode(st)
		case "/api/index":
			shipped++
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true})
		}
	}))
	defer srv.Close()

	prevSchedule, prevRecheck := shipSchedule, shipRecheckEvery
	shipSchedule, shipRecheckEvery = []time.Duration{time.Millisecond}, 20*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		shipWhenUnreadable(ctx, srv.Client(), srv.URL+"/mcp", "secret", "token", root)
	}()
	// The knobs go back only once the loop reading them has stopped.
	t.Cleanup(func() {
		cancel()
		<-done
		shipSchedule, shipRecheckEvery = prevSchedule, prevRecheck
	})

	waitFor := func(want int, why string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := shipped
			mu.Unlock()
			if n >= want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal(why)
	}
	waitFor(1, "the first shipment never happened")
	time.Sleep(100 * time.Millisecond) // several rechecks against a daemon that holds the index
	mu.Lock()
	if shipped != 1 {
		mu.Unlock()
		t.Fatalf("shipped %d times while the daemon held the index: the recheck must not re-ship what is served", shipped)
	}
	restarted = true // the daemon came back with an empty memory
	mu.Unlock()
	waitFor(2, "the daemon restarted, listed the tree unreadable again with nothing supplied, and the "+
		"bridge never shipped again: matching stays off for that tree until another registration")
}
