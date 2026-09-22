package main

import (
	"encoding/json"
	"fmt"
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
	fn, fake := piTransport(t)
	dir := filepath.Dir(fake)

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

// piTransport lifts the shipped transport out of plugins/pi/dibs.ts and
// writes a stand-in for `dibs mcp-stdio` beside it, returning the source
// to embed and the path to the fake. The subject of these tests is the
// transport's lifecycle, not the daemon's, so the fake answers id 2 and
// then stays up the way a bridge with a shipper to run does.
func piTransport(t *testing.T) (transport, fake string) {
	t.Helper()
	plugin, err := filepath.Abs(filepath.Join("..", "..", "plugins", "pi", "dibs.ts"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(plugin) // #nosec G304 -- a file in this repository
	if err != nil {
		t.Fatal(err)
	}
	// The transport itself, lifted from the shipped file so that a change
	// there is what gets tested. bridgeCall is self-contained above the
	// pi API it is embedded in.
	const from = "let lingering: ChildProcessWithoutNullStreams | undefined"
	const to = "\n/**"
	start := strings.Index(string(raw), from)
	if start < 0 {
		t.Fatal("plugins/pi/dibs.ts no longer defines a lingering bridge: registering " +
			"starts the index shipper and the shipper needs a process to run in")
	}
	end := strings.Index(string(raw)[start:], to)
	if end < 0 {
		t.Fatal("could not find the end of bridgeCall in plugins/pi/dibs.ts")
	}
	fn := string(raw)[start : start+end]

	dir := t.TempDir()
	// A stand-in for `dibs mcp-stdio`: answers id 2, then stays up the way
	// a bridge with a shipper to run does. The subject is the transport's
	// lifecycle, not the daemon's.
	fake = filepath.Join(dir, "fake-dibs")
	// DIBS_FAKE_ERROR refuses the call THE WAY THE DAEMON DOES, which is
	// the distinction round sixty turned on. An ordinary tool failure is
	// not a JSON-RPC error: internal/mcp answers with a perfectly good
	// response carrying `result.isError` and the code in its text, and a
	// refused register or resume is exactly that. The first version of
	// this fixture produced a transport error instead, which the daemon
	// almost never sends, so it passed against code that handled only
	// that shape. DIBS_FAKE_RPC_ERROR keeps the rarer one, because both
	// have to fail the same way.
	script := "#!/usr/bin/env -S bun run\n" +
		"const fail = process.env.DIBS_FAKE_ERROR === \"1\" || process.env.DIBS_FAKE_RPC_ERROR === \"1\"\n" +
		"process.stdin.setEncoding(\"utf8\")\n" +
		"process.stdin.on(\"data\", (c: string) => {\n" +
		"  for (const l of c.split(\"\\n\")) { if (!l.trim()) continue\n" +
		"    try { const m = JSON.parse(l); if (m.id !== 2) continue\n" +
		"      const toolErr = {jsonrpc:\"2.0\",id:2,result:{isError:true,content:[{type:\"text\"," +
		"text:JSON.stringify({code:\"E_RATE_LIMITED\",message:\"called Dibs recently\"})}]}}\n" +
		"      const rpcErr = {jsonrpc:\"2.0\",id:2,error:{code:-32000,message:\"E_RATE_LIMITED\"}}\n" +
		"      const body = process.env.DIBS_FAKE_RPC_ERROR === \"1\" ? rpcErr\n" +
		"                 : fail ? toolErr : {jsonrpc:\"2.0\",id:2,result:{ok:true}}\n" +
		"      process.stdout.write(JSON.stringify(body) + \"\\n\")\n" +
		"    } catch {}\n" +
		"  }\n" +
		"})\n" +
		"setInterval(() => {}, 1000)\n" // a shipper's ticker: it does not exit on its own
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil { // #nosec G306 -- must be executable
		t.Fatal(err)
	}

	return fn, fake
}

// A refused call does not kill the bridge that is already shipping.
//
// The first cut kept any child that ANSWERED, and a JSON-RPC error is an
// answer: one refused re-registration (E_RATE_LIMITED, a name already
// taken) killed the healthy bridge whose shipper was running and put in
// its place one that had registered nothing. The agent's index stops
// being shipped and the process holding its session is gone, so the next
// liveness sweep can read it as dormant and release its claims: a refusal
// that should have changed nothing takes the agent off the board. Round
// fifty-nine of the pre-release review, on round fifty-eight's own fix.
func TestARefusedPiCallLeavesTheShippingBridgeAlone(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed; the transport cannot be run")
	}
	fn, fake := piTransport(t)
	dir := filepath.Dir(fake)

	// Register (answers), then a second call the daemon refuses. The
	// first child must still be the lingering one, and still alive.
	// BOTH SHAPES OF REFUSAL. `tool` is what the daemon actually sends
	// and is the one the previous round missed; `rpc` is the transport
	// error, which is rarer and was the only one tested.
	for _, shape := range []string{"tool", "rpc"} {
		t.Run(shape+" error", func(t *testing.T) {
			refusedPiCallKeepsTheBridge(t, bun, fn, fake, dir, shape)
		})
	}
}

func refusedPiCallKeepsTheBridge(t *testing.T, bun, fn, fake, dir, shape string) {
	t.Helper()
	envVar := "DIBS_FAKE_ERROR"
	if shape == "rpc" {
		envVar = "DIBS_FAKE_RPC_ERROR"
	}
	drive := "import { spawn, type ChildProcessWithoutNullStreams } from \"node:child_process\"\n" +
		"const BIN = " + strconv.Quote(fake) + "\n" +
		fn +
		"\nconst client = { name: \"pi\", title: \"pi\", version: \"\" }\n" +
		"const ok = await bridgeCall(\"tools/call\", { name: \"register\" }, 5000, client, true)\n" +
		"if (!ok) { console.log(\"NOANSWER\"); process.exit(1) }\n" +
		"const first = lingering!.pid\n" +
		"process.env." + envVar + " = \"1\"\n" +
		"const refused = await bridgeCall(\"tools/call\", { name: \"register\" }, 5000, client, true)\n" +
		"const isRefusal = !!refused && (!!refused.error || !!refused.result?.isError)\n" +
		"if (!isRefusal) { console.log(\"NOTREFUSED \" + JSON.stringify(refused)); process.exit(1) }\n" +
		"console.log(\"PID \" + (lingering?.pid ?? 0) + \" FIRST \" + first)\n"
	path := filepath.Join(dir, "refuse-"+shape+".ts")
	if err := os.WriteFile(path, []byte(drive), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bun, "run", path) // #nosec G204 -- paths this test created
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the transport did not finish cleanly: %v\n%s", err, out)
	}
	kept, first := 0, 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if _, e := fmt.Sscanf(strings.TrimSpace(l), "PID %d FIRST %d", &kept, &first); e == nil {
			break
		}
	}
	if first == 0 {
		t.Fatalf("the register bridge was never kept, so this proves nothing: %q", out)
	}
	defer func() {
		for _, pid := range []int{kept, first} {
			if pid == 0 {
				continue
			}
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	}()
	if kept != first {
		t.Errorf("a refused call replaced the lingering bridge (%d became %d): the shipper "+
			"that was running is gone and the one in its place registered nothing",
			first, kept)
	}
	p, err := os.FindProcess(first)
	if err != nil {
		t.Fatalf("finding the bridge that registered: %v", err)
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("a refused call killed the bridge that registered (%v): its index stops "+
			"shipping and the next liveness sweep can read the agent as dormant and "+
			"release its claims", err)
	}
}

// And the list of calls that keep their bridge is the bridge's own.
//
// `shipIndexOnRegister` starts a shipper for register, for resume, and
// for an update that moves the agent; the first cut of this kept only
// register, so a recovered or relocated agent lost its shipper again.
// Worse than lost: resume ROTATES the token, so the register bridge kept
// for its shipper goes on shipping with a revoked one, which is a 401 for
// as long as the session lasts. Round fifty-nine of the pre-release
// review.
func TestThePiTransportKeepsABridgeForEveryShipperStartingCall(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "pi", "dibs.ts")) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	// The plugin's own predicate, lifted and run, against the four cases
	// indexship.go distinguishes.
	const marker = "function startsAShipper(tool: string, args: Record<string, unknown>): boolean {"
	start := strings.Index(string(src), marker)
	if start < 0 {
		t.Fatal("plugins/pi/dibs.ts no longer decides which calls keep their bridge: " +
			"resume and a relocating update start a shipper too, and a shipper with no " +
			"process never runs")
	}
	end := strings.Index(string(src)[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of startsAShipper")
	}
	fn := string(src)[start : start+end+3]

	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed")
	}
	script := fn + "\nconsole.log(JSON.stringify([\n" +
		"  startsAShipper(\"register\", {}),\n" +
		"  startsAShipper(\"resume\", {}),\n" +
		"  startsAShipper(\"update\", { cwd: \"/w/repo\" }),\n" +
		"  startsAShipper(\"update\", {}),\n" +
		"  startsAShipper(\"claim\", { path: \"/w/repo/f.go\" }),\n" +
		"]))\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "which.ts")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bun, "run", path).Output() // #nosec G204 -- paths this test created
	if err != nil {
		t.Fatalf("running the plugin's rule: %v\n%s", err, out)
	}
	var got []bool
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("the rule did not return booleans: %v\n%s", err, out)
	}
	want := []bool{true, true, true, false, false}
	if len(got) != len(want) {
		t.Fatalf("got %d answers for %d cases", len(got), len(want))
	}
	names := []string{"register", "resume", "update with a cwd", "update with none", "claim"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: keeps its bridge = %v, want %v. The list is cmd/dibs/indexship.go's "+
				"(shipIndexOnRegister); too few and the shipper has no process to run in, "+
				"too many and pi leaves one behind per call.", names[i], got[i], want[i])
		}
	}
}
