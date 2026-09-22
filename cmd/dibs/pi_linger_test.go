package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Registering through the pi extension leaves its bridge running, and pi
// still exits.
//
// Both halves or neither. The extension spawns `dibs mcp-stdio` per call
// and kills it at the answer, which is right for a guard and wrong for
// register: registering starts the bridge's index shipper, whose first
// look at the daemon's verdict is three seconds out (indexship.go,
// shipSchedule), so pi registered successfully and shipped nothing, ever.
// A checkout the daemon cannot read itself, on a peer machine or behind
// macOS's file access, then has no semantic matching at all and nothing
// says so. opencode escapes this because it ALSO runs the bridge as its
// MCP server; pi has no such process. Round fifty-eight of the pre-release
// review, on the transport rewritten in the round before it.
//
// The other half is why the transport stopped keeping a bridge in the
// first place: a piped child holds the parent's event loop, under bun
// however it is unref'd, and the harness would not exit. So this drives
// the real file under the real runtime and waits for the process to END.
// A regression in either direction fails here: no lingering child, or a
// harness that hangs (this test times out rather than passing slowly).
//
// Shown failing, not assumed: with the transport's `linger` off, this
// fixture reports "no lingering bridge was reported: PID 0". Stated that
// way because the pre-fix file has no such parameter at all, so the
// honest demonstration is the flag inside the fixture rather than a
// worktree at the previous commit, where the marker check fires first.
func TestPiKeepsTheBridgeThatRegisteredAndStillExits(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed; the transport cannot be run")
	}
	plugin, err := filepath.Abs(filepath.Join("..", "..", "plugins", "pi", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(plugin) // #nosec G304 -- a file in this repository
	if err != nil {
		t.Fatal(err)
	}
	// The transport itself, lifted from the shipped file so that a change
	// there is what gets tested. bridgeCall is self-contained above the
	// pi API it is embedded in.
	const from = "let lingering: ChildProcessWithoutNullStreams | undefined"
	const to = "\n/**"
	start := strings.Index(string(src), from)
	if start < 0 {
		t.Fatal("plugins/pi/dibs.ts no longer defines a lingering bridge: registering " +
			"starts the index shipper and the shipper needs a process to run in")
	}
	end := strings.Index(string(src)[start:], to)
	if end < 0 {
		t.Fatal("could not find the end of bridgeCall in plugins/pi/dibs.ts")
	}
	fn := string(src)[start : start+end]

	dir := t.TempDir()
	// A stand-in for `dibs mcp-stdio`: answers id 2, then stays up the way
	// a bridge with a shipper to run does. The subject is the transport's
	// lifecycle, not the daemon's.
	fake := filepath.Join(dir, "fake-dibs")
	script := "#!/usr/bin/env -S bun run\n" +
		"const lines: string[] = []\n" +
		"process.stdin.setEncoding(\"utf8\")\n" +
		"process.stdin.on(\"data\", (c: string) => {\n" +
		"  for (const l of c.split(\"\\n\")) { if (!l.trim()) continue; lines.push(l)\n" +
		"    try { const m = JSON.parse(l); if (m.id === 2) " +
		"process.stdout.write(JSON.stringify({jsonrpc:\"2.0\",id:2,result:{ok:true}}) + \"\\n\") } catch {}\n" +
		"  }\n" +
		"})\n" +
		"setInterval(() => {}, 1000)\n" // a shipper's ticker: it does not exit on its own
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil { // #nosec G306 -- must be executable
		t.Fatal(err)
	}

	drive := "import { spawn, type ChildProcessWithoutNullStreams } from \"node:child_process\"\n" +
		"const BIN = " + strconv.Quote(fake) + "\n" +
		fn +
		"\nconst client = { name: \"pi\", title: \"pi\", version: \"\" }\n" +
		"const r = await bridgeCall(\"tools/call\", { name: \"register\" }, 5000, client, true)\n" +
		"if (!r) { console.log(\"NOANSWER\"); process.exit(1) }\n" +
		"console.log(\"PID \" + (lingering?.pid ?? 0))\n"
	path := filepath.Join(dir, "drive.ts")
	if err := os.WriteFile(path, []byte(drive), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bun, "run", path) // #nosec G204 -- paths this test created
	out, err := cmd.CombinedOutput()      // returns only when the harness EXITS
	if err != nil {
		t.Fatalf("the transport did not finish cleanly: %v\n%s", err, out)
	}
	line := strings.TrimSpace(string(out))
	pid := 0
	for _, l := range strings.Split(line, "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(l), "PID "); ok {
			pid, _ = strconv.Atoi(after)
		}
	}
	if pid == 0 {
		t.Fatalf("no lingering bridge was reported: %q", line)
	}
	defer func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}()
	// Signal 0: alive, and still alive AFTER the parent exited, which is
	// the whole point. os.FindProcess never fails on unix, so the signal
	// is the test.
	p, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("finding the lingering bridge: %v", err)
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the bridge that registered is gone (%v): pi registers and its index "+
			"shipper never runs, so a checkout the daemon cannot read gets no matching "+
			"and nothing reports it", err)
	}
}
