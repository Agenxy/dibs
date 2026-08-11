package liveness

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// The environment IS readable for the processes this has to work on, and the
// exception is precise rather than mysterious.
//
// Measured on macOS: the environment of an Apple-signed PLATFORM binary is
// hidden. /bin/sleep, a copy of it, and any script run by /bin/bash all show
// nothing. The environment of a user-installed binary is visible. Every agent
// harness is the second kind (codex, claude, opencode), which is why a real
// `codex exec` exposed its whole environment while three attempts to fake one
// with system tools exposed none.
//
// A harness invoked through a shell still works: the shell is a platform binary
// and hides ITS environment, but the agent it execs is a user binary and shows
// the variable it inherited. That is the actual chain the PreToolUse stamp
// creates, and it is the one measured here.
//
// The `-o command=` selector, incidentally, does NOT hide the environment: with
// the `e` flag, `command` INCLUDES it. That was an earlier wrong diagnosis of
// this same symptom, made by testing the theory against /bin/sleep, where the
// environment is hidden either way.
//
// The subject is this test binary: compiled by `go test`, so user-installed by
// construction, on every platform and every machine. That makes the assertion
// unconditional rather than "skip if the platform is shy", which would have
// hidden a real regression behind an environmental excuse.
func TestTheEnvironmentIsReadableForUserBinaries(t *testing.T) {
	c := exec.Command(os.Args[0], "-test.run=TestHelperStaysAlive", "-test.timeout=90s")
	c.Env = append(os.Environ(), "LANES_PARENT=sentinel-lane", "LANES_TEST_HELPER=1")
	if err := c.Start(); err != nil {
		t.Fatalf("could not spawn a user binary to inspect: %v", err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()

	// The child may exit quickly; read while it is certainly alive.
	blob := EnvironOf(c.Process.Pid)
	if !strings.Contains(blob, "LANES_PARENT=sentinel-lane") {
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			t.Skipf("environment reading is not implemented for %s", runtime.GOOS)
		}
		t.Fatalf("a user-compiled binary did not expose LANES_PARENT.\n"+
			"  This is the channel the whole attribution design rests on, and it is\n"+
			"  supposed to work for exactly this kind of process.\n  got: %.200q", blob)
	}
	owner, via := attribute(c.Process.Pid, 0)
	if owner != "sentinel-lane" || via != "env" {
		t.Errorf("the environment was readable and the stamp was ignored: owner=%q via=%q\n"+
			"  reading it and not using it is the same outcome as not reading it", owner, via)
	}
}

// A pid that does not exist must produce nothing, not a stale or borrowed
// answer from whatever ps prints on failure.
func TestNoProcessMeansNoEnvironment(t *testing.T) {
	if blob := EnvironOf(0); blob != "" {
		t.Errorf("pid 0 produced %q", blob)
	}
	if owner, _ := attribute(-1, 0); owner != "" {
		t.Errorf("an invalid pid was attributed to %q", owner)
	}
}

// TestHelperStaysAlive is not a test; it is the subject of one.
//
// A user-compiled process that stays alive long enough to be inspected twice.
// The first version of the test above pointed its child at a real test, which
// finished in milliseconds, so the environment read succeeded and the
// attribution that followed sampled a process that had already exited. The
// symptom was "readable but ignored", which reads exactly like a broken regex.
func TestHelperStaysAlive(t *testing.T) {
	if os.Getenv("LANES_TEST_HELPER") == "" {
		t.Skip("not the helper invocation")
	}
	// Blocks until the parent kills it. A sleep would be banned here (and
	// rightly: a test that sleeps is a test that is guessing); this is not
	// waiting for anything to happen, it is being a process that exists.
	select {}
}
