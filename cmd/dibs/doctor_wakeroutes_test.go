package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An operator is told when the only wake route they have cannot be confirmed.
//
// There are two routes and they are not equals. `[wake.exec]` starts a process
// and the daemon sees its exit status. The session socket needs no setup and is
// best effort: the receiving harness decides whether to accept a peer message
// and sends no receipt, so a notice that was HELD looks exactly like one that
// was read.
//
// That difference was invisible, and doctor is the tool whose whole job is
// finding what is quietly broken. Measured on the machine this is developed on:
// notices delivered to an idle live session, every write succeeding, nothing
// arriving, because a Claude Code session in bypassPermissions mode holds peer
// messages for its human. An unattended fleet runs in exactly that mode.
func TestDoctorSaysWhichWakeRoutesExist(t *testing.T) {
	run := func(t *testing.T, toml string, board *boardView) (oks, warns []string) {
		t.Helper()
		dir := t.TempDir()
		if toml != "" {
			if err := os.WriteFile(filepath.Join(dir, "dibs.toml"), []byte(toml), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		checkWakeRoutes(dir, board,
			func(s string) { oks = append(oks, s) },
			func(s, fix string) { warns = append(warns, s+" || "+fix) })
		return
	}

	t.Run("no command configured", func(t *testing.T) {
		oks, warns := run(t, "", nil)
		if len(warns) != 1 {
			t.Fatalf("expected one warning, got oks=%v warns=%v", oks, warns)
		}
		for _, want := range []string{"best effort", "bypassPermissions", "wake.exec"} {
			if !strings.Contains(warns[0], want) {
				t.Errorf("the warning does not mention %q, so it does not tell the "+
					"operator what is actually wrong: %s", want, warns[0])
			}
		}
	})

	t.Run("a command that covers every agent", func(t *testing.T) {
		oks, warns := run(t, "[wake.exec.claude]\nargv = [\"echo\", \"{message}\"]\n",
			boardOf(agentRow("a", "persistent", "claude"), agentRow("b", "persistent", "CLAUDE")))
		if len(warns) != 0 {
			t.Errorf("warned about a board every one of whose agents has a route: %v", warns)
		}
		if len(oks) != 1 || !strings.Contains(oks[0], "covering all 2") {
			t.Errorf("did not report the coverage: %v", oks)
		}
	})

	// THE CASE THE OLD CHECK CALLED HEALTHY.
	//
	// It counted configured commands, so one [wake.exec] block reported a tick
	// however many agents it left with no route. On the board this was written
	// against that was twelve Claude Code agents unreachable behind a green
	// line. Configuring something is not covering anything.
	t.Run("a command that covers only some agents", func(t *testing.T) {
		oks, warns := run(t, "[wake.exec.codex]\nargv = [\"echo\", \"{message}\"]\n",
			boardOf(agentRow("a", "persistent", "codex"),
				agentRow("b", "persistent", "Claude Code"),
				agentRow("c", "persistent", "Claude Code")))
		if len(oks) != 0 {
			t.Errorf("reported healthy while two agents cannot be woken: %v", oks)
		}
		if len(warns) != 1 {
			t.Fatalf("expected one warning, got %v", warns)
		}
		for _, want := range []string{"2 of 3", "claude code (2)", `[wake.exec."claude code"]`} {
			if !strings.Contains(warns[0], want) {
				t.Errorf("the warning is missing %q, so it does not say who is stranded "+
					"or what to write: %s", want, warns[0])
			}
		}
	})

	// An ephemeral agent ends with its session, so nobody expects to wake one
	// and counting it as stranded would report a fault on every correct board.
	t.Run("ephemeral agents are not counted", func(t *testing.T) {
		oks, warns := run(t, "[wake.exec.codex]\nargv = [\"echo\", \"{message}\"]\n",
			boardOf(agentRow("a", "persistent", "codex"),
				agentRow("scratch", "ephemeral", "Claude Code")))
		if len(warns) != 0 {
			t.Errorf("counted an ephemeral agent as needing a wake route: %v", warns)
		}
		if len(oks) != 1 || !strings.Contains(oks[0], "all 1 persistent") {
			t.Errorf("expected coverage of the one persistent agent: %v", oks)
		}
	})

	// A COMMAND IS NOT A ROUTE ON ITS OWN.
	//
	// wakeRoute refuses the exec path when the agent holds no UUID-shaped
	// thread id, because `--resume` and `exec resume` both need something to
	// name. Counting such an agent as covered because its HARNESS has a command
	// is the same optimism as counting configured commands and calling it
	// coverage: the report would be green and the agent still unreachable.
	t.Run("a covered harness with no thread to resume", func(t *testing.T) {
		stranded := agentRow("no-thread", "persistent", "codex")
		stranded.Resumable = false
		oks, warns := run(t, "[wake.exec.codex]\nargv = [\"echo\", \"{message}\"]\n",
			boardOf(agentRow("fine", "persistent", "codex"), stranded))
		if len(oks) != 0 {
			t.Errorf("called an agent covered when nothing can name its thread: %v", oks)
		}
		if len(warns) != 1 || !strings.Contains(warns[0], "no resumable thread") {
			t.Fatalf("did not say WHY that agent is stranded: %v", warns)
		}
		// And it must not tell them to add a block they already have.
		if strings.Contains(warns[0], "[wake.exec.codex]") {
			t.Errorf("told the operator to add a command that is already configured, "+
				"which is how a report loses the reader's trust: %s", warns[0])
		}
	})

	// Dibs's own rows are not threads, and reporting them as unreachable is the
	// false alarm that teaches an operator to skim this check.
	t.Run("the human and the daemon are not counted", func(t *testing.T) {
		b := boardOf(agentRow("a", "persistent", "codex"))
		human := agentRow("lael", "persistent", "dibs web")
		human.Human = true
		human.Agent.Surface = "web"
		daemon := agentRow("dibs", "persistent", "dibs")
		daemon.Agent.Surface = "daemon"
		b.Agents = append(b.Agents, human, daemon)
		oks, warns := run(t, "[wake.exec.codex]\nargv = [\"echo\", \"{message}\"]\n", b)
		if len(warns) != 0 {
			t.Errorf("counted Dibs's own rows as agents needing a wake: %v", warns)
		}
		if len(oks) != 1 || !strings.Contains(oks[0], "all 1 persistent") {
			t.Errorf("expected coverage of the one real agent: %v", oks)
		}
	})
}

// agentRow is one board row with just the fields wake coverage reads.
func agentRow(id, kind, harness string) boardAgent {
	// Resumable by default: the common case is an agent that registered through
	// its harness's plugin and therefore holds a thread id. The case that does
	// not has its own subtest.
	a := boardAgent{ID: id, Kind: kind, Status: "dormant", Resumable: true}
	a.Agent = &struct {
		Harness string `json:"harness,omitempty"`
		CWD     string `json:"cwd,omitempty"`
		Surface string `json:"surface,omitempty"`
	}{Harness: harness}
	return a
}

func boardOf(rows ...boardAgent) *boardView {
	return &boardView{Agents: rows}
}
