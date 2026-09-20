package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go shipWhenUnreadable(ctx, srv.Client(), srv.URL+"/mcp", "secret", &shipper{token: "token"}, "/not/a/checkout",
		shipTiming{schedule: []time.Duration{time.Millisecond}, recheck: time.Hour})
	time.Sleep(200 * time.Millisecond)

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
	// And a supplied index at this path from ANOTHER machine is not ours: the
	// daemon serves it to that machine only, so this bridge still ships and
	// hears the daemon's refusal rather than staying silent. Round twelve.
	st := matchStatusJSON{
		Remote: []string{root}, Supplied: map[string]string{root: "them"},
		SuppliedHosts: map[string]string{root: "machine-a"}, Host: "hub",
	}
	if suppliedFor(st, root, "machine-b") {
		t.Error("another machine's index at this path was read as this machine's")
	}
	if !suppliedFor(st, root, "machine-a") {
		t.Error("this machine's own shipment was not recognised")
	}
	local := matchStatusJSON{Unreadable: []string{root}, Supplied: map[string]string{root: "me"}, Host: "hub"}
	if !suppliedFor(local, root, "") || !suppliedFor(local, root, "hub") {
		t.Error("a local shipment was not recognised by a local bridge")
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

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go shipWhenUnreadable(ctx, srv.Client(), srv.URL+"/mcp", "secret", &shipper{token: "token"}, root,
		shipTiming{schedule: []time.Duration{time.Millisecond}, recheck: 20 * time.Millisecond})

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

// The shipper watches the directory the agent REGISTERED, not the one the
// bridge runs in, and an update that moves the agent starts one for the new
// tree. Round seven of the pre-release review.
func TestTheShipperFollowsTheRegisteredDirectory(t *testing.T) {
	// Two checkouts; the bridge's own directory is neither.
	mk := func() string {
		t.Helper()
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
		return root
	}
	registered, moved := mk(), mk()

	asked, tokens := make(chan string, 16), make(chan string, 16)
	restarted := make(chan struct{}, 1)
	var mu sync.Mutex
	supplied := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		select {
		case <-restarted:
			supplied = map[string]bool{} // a restart forgets every supplied index
		default:
		}
		if r.URL.Path == "/api/match-status" {
			// Whatever tree is asked about is one to ship for, unless served.
			st := matchStatusJSON{Unreadable: []string{registered, moved}, Supplied: map[string]string{}}
			for root := range supplied {
				st.Supplied[root] = "a"
			}
			_ = json.NewEncoder(w).Encode(st)
			return
		}
		var body struct {
			Root  string `json:"root"`
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		supplied[body.Root] = true
		asked <- body.Root
		tokens <- body.Token
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true})
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	timing := shipTiming{schedule: []time.Duration{time.Millisecond}, recheck: 20 * time.Millisecond}
	hook := shipIndexOnRegister(ctx, srv.Client(), srv.URL+"/mcp", "secret", timing, func([]byte, []byte) {})
	// A reply as the daemon writes it: the board carries the directory it
	// recorded for the agent, which is what the shipper watches.
	replyFor := func(id int, tok, cwd string) []byte {
		inner, _ := json.Marshal(map[string]any{
			"token": tok, "agent_id": "a",
			"board": map[string]any{"agents": []map[string]any{{"id": "a", "agent": map[string]any{"cwd": cwd}}}},
		})
		outer, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": id,
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": string(inner)}}},
		})
		return outer
	}
	sent := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"register","arguments":{"name":"a"}}}`)
	hook(sent, replyFor(1, "tok", registered))
	select {
	case root := <-asked:
		if root != registered {
			t.Fatalf("the first shipment was for %q, want the registered directory %q (the bridge runs in neither)", root, registered)
		}
		<-tokens
	case <-time.After(5 * time.Second):
		t.Fatal("no shipment for the registered directory")
	}

	// The credential rotates on a resume; the shipper for the same tree
	// ships with the new one, or every later shipment is a 401. Round eight
	// of the pre-release review.
	// resume takes no cwd; the reply's board says where the agent is.
	resumeOp := []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"resume","arguments":{"nonce":"n","resume_id":"r"}}}`)
	hook(resumeOp, replyFor(3, "tok-2", registered))
	restarted <- struct{}{} // the daemon forgets the index; the shipper must ship again, with tok-2
	select {
	case <-asked:
		if got := <-tokens; got != "tok-2" {
			t.Fatalf("after the resume the shipment carried token %q, want the rotated tok-2: the old one is revoked and the daemon answers 401", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no shipment after the daemon forgot the index")
	}

	moveOp := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"update","arguments":{"token":"tok-2","cwd":` + strconv.Quote(moved) + `}}}`)
	hook(moveOp, []byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"ok\":true}"}]}}`))
	select {
	case root := <-asked:
		if root != moved {
			t.Fatalf("after the agent moved, the shipment was for %q, want %q", root, moved)
		}
		<-tokens
	case <-time.After(5 * time.Second):
		t.Fatal("no shipment for the directory the agent moved to")
	}
}

// A status request that stalls does not hold the shipper past its
// cancellation. The loop asks through the bridge's streaming client, which
// has no timeout of its own, and the request was built without a context:
// a daemon that accepted the connection and never answered held every
// later recheck and shipment for that tree, and cancelling the shipper did
// not end the request. Round twelve of the pre-release review.
func TestAStalledStatusRequestDoesNotOutliveTheShipper(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hold // never answers until the test lets go
	}))
	t.Cleanup(func() { close(hold); srv.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		fetchMatchStatusCtx(ctx, srv.Client(), srv.URL, "secret")
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the status request outlived its context: a stalled daemon holds the shipper forever")
	}
}

// A shipment survives the bridge replacing itself.
//
// The shippers lived in a map local to the registration hook, so the
// upgrade handoff (which carries the handshake, the subscriptions and the
// self-wake) could not see them: the next image shipped nothing until the
// agent happened to register, resume or move, and a daemon restarted in
// between held no index for its tree, so matching there was off for as long
// as the agent kept making ordinary calls. The handoff now carries every
// tree and its credential, and the next image restarts them. Round
// twenty-six of the pre-release review.
func TestAShipmentSurvivesTheBridgeUpgrade(t *testing.T) {
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

	shipped := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/match-status" {
			_ = json.NewEncoder(w).Encode(matchStatusJSON{Unreadable: []string{root}}) // never served: always wanted
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		shipped <- body.Token
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true})
	}))
	defer srv.Close()

	// The old image: a register through the hook starts a shipment.
	shipments.Lock()
	shipments.byRoot = nil
	shipments.Unlock()
	t.Cleanup(func() {
		shipments.Lock()
		shipments.byRoot = nil
		shipments.Unlock()
	})
	oldCtx, endOld := context.WithCancel(context.Background())
	timing := shipTiming{schedule: []time.Duration{time.Millisecond}, recheck: time.Hour}
	hook := shipIndexOnRegister(oldCtx, srv.Client(), srv.URL+"/mcp", "secret", timing, func([]byte, []byte) {})
	inner, _ := json.Marshal(map[string]any{
		"token": "tok-old", "agent_id": "a",
		"board": map[string]any{"agents": []map[string]any{{"id": "a", "agent": map[string]any{"cwd": root}}}},
	})
	reply, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1,
		"result": map[string]any{"content": []map[string]any{{"type": "text", "text": string(inner)}}},
	})
	hook([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"register","arguments":{"name":"a"}}}`), reply)
	select {
	case <-shipped:
	case <-time.After(5 * time.Second):
		t.Fatal("setup: the old image never shipped")
	}
	// What the old image hands over on exec, and the old image goes.
	carried := handoffState().Shipments
	if len(carried) != 1 || carried[0].Root != root || carried[0].Token != "tok-old" {
		t.Fatalf("the handoff carries %+v, want the one shipment with its credential: the next image "+
			"would ship nothing until the agent registered again", carried)
	}
	endOld()
	shipments.Lock()
	shipments.byRoot = nil // a fresh process
	shipments.Unlock()

	// The new image restores what it was handed and ships for it.
	newCtx, endNew := context.WithCancel(context.Background())
	defer endNew()
	restoreShipments(newCtx, srv.Client(), srv.URL+"/mcp", "secret", carried, timing)
	select {
	case tok := <-shipped:
		if tok != "tok-old" {
			t.Fatalf("the restored shipment carried token %q, want the one handed over", tok)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the upgraded bridge never shipped for the tree the old image was shipping for")
	}
}
