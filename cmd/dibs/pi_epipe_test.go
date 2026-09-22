package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A bridge that dies mid-write does not take the harness down with it.
//
// The transport writes three lines to the child's stdin. A body big
// enough to exceed the pipe buffer completes ASYNCHRONOUSLY, so a bridge
// that exits meanwhile (no daemon, a failed preflight, a bad config)
// raises EPIPE on that stream after the try/catch around the writes has
// already returned. An 'error' event with no listener is an uncaught
// exception, and the runtime ends the process: the plugin's one promise,
// that Dibs being down costs the agent nothing, instead costs it the
// whole session. Round sixty-three of the pre-release review.
//
// UNDER NODE, WHICH IS THE RUNTIME THAT DIES. The first version of this
// drove bun, saw it survive, and would have reported the fix as verified
// against code that has the defect: bun does not end the process on an
// unhandled stream error and Node does, and pi runs under Node. Measured
// both ways. The assertion is the harness's EXIT STATUS, because that is
// the failure: a plugin returning null is fine, a dead pi is not.
func TestABridgeThatDiesMidWriteDoesNotKillTheHarness(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the runtime that dies cannot be run")
	}
	for _, plugin := range []string{"pi", "opencode"} {
		t.Run(plugin, func(t *testing.T) {
			bridgeDeathIsSurvivable(t, node, plugin)
		})
	}
}

func bridgeDeathIsSurvivable(t *testing.T, node, plugin string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "..", "plugins", plugin, "dibs.ts")) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	const from = "function bridgeCall("
	start := strings.Index(string(src), from)
	if start < 0 {
		t.Fatalf("plugins/%s/dibs.ts no longer defines bridgeCall", plugin)
	}
	end := strings.Index(string(src)[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of bridgeCall in plugins/%s/dibs.ts", plugin)
	}
	fn := string(src)[start : start+end+3]
	if !strings.Contains(fn, `proc.stdin.on("error"`) {
		t.Fatalf("plugins/%s/dibs.ts has no error listener on the bridge's stdin: an "+
			"asynchronous EPIPE there is an uncaught exception and ends the harness", plugin)
	}

	dir := t.TempDir()
	// A "bridge" that accepts a little and then goes away, which is what
	// a real binary failing its preflight does: the pipe has to be OPEN
	// when the write starts, or the write fails synchronously inside the
	// try/catch and the defect never fires. A child that exits at once
	// survives even the unfixed transport, which is how the first
	// version of this test passed against the bug.
	fake := filepath.Join(dir, "gone")
	script := "#!/bin/sh\nhead -c 200 >/dev/null 2>&1\nsleep 0.05\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil { // #nosec G306
		t.Fatal(err)
	}
	// Big enough to exceed the pipe buffer, so the write completes after
	// the synchronous try/catch has returned.
	drive := "import { spawn, type ChildProcessWithoutNullStreams } from \"node:child_process\"\n" +
		"const BIN = " + strconv.Quote(fake) + "\n" +
		fn +
		"\nconst big = \"x\".repeat(200000)\n" +
		"const r = await bridgeCall(\"tools/call\", { name: \"declare\", arguments: { text: big } }, " +
		"2000, { name: \"t\", title: \"t\", version: \"\" })\n" +
		"console.log(\"answered:\", r === null ? \"null\" : \"something\")\n" +
		"await new Promise((res) => setTimeout(res, 300))\n" + // let a late EPIPE land
		"console.log(\"SURVIVED\")\n"
	path := filepath.Join(dir, "epipe-"+plugin+".ts")
	if err := os.WriteFile(path, []byte(drive), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "--experimental-strip-types", path) // #nosec G204 -- paths this test created
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the harness died because the bridge did (%v): a daemon that is down, or a "+
			"dibs that fails its preflight, ends the agent's session instead of costing "+
			"it one silent call\n%s", err, out)
	}
	if !strings.Contains(string(out), "SURVIVED") {
		t.Fatalf("the harness did not reach the end of its script: %q", out)
	}
}
