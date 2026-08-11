package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// An admin command must not write one install's state while addressing another.
//
// This is a real accident, not a hypothetical: `lanes admin prune` and `lanes
// admin role` act on the daemon at LANES_ADDR, while `set-password` writes a
// FILE and resolved it from LANES_DIR. Pointing the CLI at a second daemon,
// which means setting LANES_ADDR, the obvious half: silently overwrote the
// admin password of the FIRST one. It printed the path it wrote, and the path
// was correct, and it was still the wrong install.
//
// The local secret is the install's identity, so the two can be compared
// exactly rather than guessed at.
func TestSetPasswordRefusesADirectoryTheAddressedDaemonDisowns(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("this-dirs-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A daemon belonging to some OTHER data directory: it does not recognise
	// this one's secret, which is exactly how a second install presents itself.
	stranger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Lanes-Local") != "the-other-dirs-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer stranger.Close()

	t.Setenv("LANES_DIR", dir)
	t.Setenv("LANES_ADDR", strings.TrimPrefix(stranger.URL, "http://"))

	err := confirmDirIsTheAddressedDaemon()
	if err == nil {
		t.Fatal("wrote a password into a directory the addressed daemon disowns , \n" +
			"  this is the check that stops `LANES_ADDR=other lanes admin set-password`\n" +
			"  from rewriting the credentials of the install you are NOT looking at")
	}
	// The operator has to be able to see WHICH two things disagreed, or the
	// refusal is just an obstacle.
	if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "LANES_DIR") {
		t.Errorf("the refusal must name the directory and how to fix it, got: %v", err)
	}
}

// The matching install must still go through, or the guard has replaced a
// silent wrong write with a loud refusal to do anything at all.
func TestSetPasswordAllowsTheDaemonThatOwnsTheDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("matching-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Lanes-Local") != "matching-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// 404 is what the real daemon returns here: the route does not exist,
		// but the secret was accepted. Anything that is not 401 means "this
		// directory is mine", and the check must not read 404 as a rejection.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer own.Close()

	t.Setenv("LANES_DIR", dir)
	t.Setenv("LANES_ADDR", strings.TrimPrefix(own.URL, "http://"))
	if err := confirmDirIsTheAddressedDaemon(); err != nil {
		t.Fatalf("refused the daemon that owns this directory: %v", err)
	}
}

// Setting a password before the first start is legitimate, and a guard that
// requires a running daemon would break the very first thing a new user does.
func TestSetPasswordProceedsWhenNothingIsListening(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "local.secret"), []byte("s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LANES_DIR", dir)
	// Port 1 on loopback: reserved, never bound, refuses immediately.
	t.Setenv("LANES_ADDR", "127.0.0.1:1")
	if err := confirmDirIsTheAddressedDaemon(); err != nil {
		t.Fatalf("blocked an offline set-password, which is how installs begin: %v", err)
	}
}

// A command that exists, works, and is not in the usage text does not exist.
//
// This codebase has shipped that shape repeatedly: a validated schema nothing
// called, a tool no agent could discover, a bootstrap path behind an early
// return. `lanes probe` is aimed at agents, and an agent's only route to it is
// the usage text, so being absent there is the same as not being built.
func TestProbeIsReachableAndDocumented(t *testing.T) {
	if !strings.Contains(usage, "lanes probe") {
		t.Error("lanes probe is not in the usage text: an agent cannot discover it,\n" +
			"  and the usage text is the only place it would look")
	}
	// And it must be listed as lane-safe: it reads a process and a file, takes
	// no token, and mutates nothing, so refusing it outside a terminal would
	// block the exact caller it is for.
	if !strings.Contains(usage, "agent-safe") {
		t.Fatal("the usage text no longer marks the agent-safe section")
	}
	_, fromAgentSafe, _ := strings.Cut(usage, "agent-safe")
	agentSafe, _, _ := strings.Cut(fromAgentSafe, "\n\n")
	if !strings.Contains(agentSafe, "lanes probe") {
		t.Error("lanes probe is documented outside the agent-safe section, so an agent\n" +
			"  reading the usage will believe it must not call it")
	}
}

// The command must use the package's defaults, not a Config built from flags.
//
// `lanes probe` constructed liveness.Config{Quiet: ..., Frozen: ...} from its
// two flags, which silently zeroed the two fields that have no flag. A zero
// MinDuty means "no process is ever idle enough to convict", so the check that
// identifies a stalled agent immediately was disabled: the package answered
// "stuck" for a real 7h39m stall while the command answered "unknown".
func TestProbeStartsFromTheLibraryDefaults(t *testing.T) {
	src, err := os.ReadFile("probe.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "liveness.DefaultConfig()") {
		t.Error("probe does not start from liveness.DefaultConfig()\n" +
			"  building a Config field-by-field from flags zeroes every field that has\n" +
			"  no flag, and those fields are thresholds: a zero threshold silently\n" +
			"  turns a check off rather than failing")
	}
}

// Every subcommand the dispatch accepts must appear in the usage text.
//
// The usage text is the only place a person (or an agent) looks. A command
// that works and is not listed does not exist to them, which is this project's
// most-repeated bug in a different costume: `lanes probe` shipped undiscovered,
// and `hook-spawn` and `mcp-stdio` were still missing when this was written,
// even though a harness config that NAMES one sends its reader straight to
// `lanes --help` to find out what it is.
//
// Machine-facing commands stay listed for exactly that reason, under a heading
// that says who calls them.
func TestEverySubcommandIsInTheUsageText(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	handled := regexp.MustCompile(`(?m)^\tcase "([a-z-]+)"`).FindAllStringSubmatch(text, -1)
	if len(handled) < 10 {
		t.Fatalf("found only %d subcommands: the dispatch pattern changed and this "+
			"test is now checking almost nothing", len(handled))
	}
	for _, m := range handled {
		if !strings.Contains(usage, "lanes "+m[1]) {
			t.Errorf("`lanes %s` is dispatched but absent from the usage text.\n"+
				"  Nobody can find it, including the agent reading --help to learn what a\n"+
				"  hook in their config does.", m[1])
		}
	}
}
