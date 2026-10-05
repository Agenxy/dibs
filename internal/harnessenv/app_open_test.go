package harnessenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This is deliberately a subprocess: reaching the production opener in a
// Go test must fail the test rather than activate the operator's app, even if
// a caller copied RealShower before its TestMain installed a fake.
func TestRealAppOpenerFailsClosedInGoTest(t *testing.T) {
	if os.Getenv("DIBS_TEST_UNFAKED_REAL_OPEN") == "1" {
		// Invalid thread ID: on pre-fix code this fails validation without
		// executing /usr/bin/open, so the negative control is desktop-safe.
		_ = RealShower.Open([]string{"/usr/bin/open", "-g", "codex://threads/not-a-uuid"})
		t.Fatal("real app opener returned in a Go test")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRealAppOpenerFailsClosedInGoTest$")
	cmd.Env = append(os.Environ(), "DIBS_TEST_UNFAKED_REAL_OPEN=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "unfaked real app opener in a Go test") {
		t.Fatalf("unfaked opener did not fail the test: %v %s", err, out)
	}
}

func TestBuiltChildInheritsAppOpenRefusal(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "app-open-guard")
	build := exec.Command("go", "build", "-o", bin, "./testdata/appopenguard")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build child opener probe: %v %s", err, out)
	}
	child := exec.Command(bin)
	child.Env = append(os.Environ(), "DIBS_TEST_FORBID_APP_OPEN=1")
	out, err := child.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "real app opener forbidden by DIBS_TEST_FORBID_APP_OPEN") {
		t.Fatalf("built child did not receive app-open refusal: %v %s", err, out)
	}
}

// Enter the production Shower and its real ownership probe, replacing only
// the fixed OS contact and actual open. #304/7428955 repaired a probe that
// falsely read live threads as unloaded; UNKNOWN must never buy a new open
// on every message. This guard compiles on the production before this fix.
func TestUnknownProbeCannotRepeatAppSwitching304(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	original := appProbeOutput
	t.Cleanup(func() { appProbeOutput = original })
	appProbeOutput = func(context.Context, string, ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}
	s := RealShower
	var opens atomic.Int32
	s.Open = func(argv []string) error {
		if len(argv) != 3 || argv[1] != "-g" {
			t.Errorf("wake did not request background open: %q", argv)
		}
		opens.Add(1)
		return nil
	}
	s.Away = func() (bool, bool) { return true, true }
	s.Wait = func(time.Duration) { t.Error("ChatGPT open waited for presence") }
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			s.ShowWhenIdle(ChatGPTOpenArgv("unknown-304"), "unknown-304", func(_, deferred bool, err error) {
				if deferred || err != nil {
					t.Errorf("prompt open: deferred=%v err=%v", deferred, err)
				}
			})
		})
	}
	wg.Wait()
	if opens.Load() != 1 {
		t.Fatalf("rapid failing probes produced %d opens, want exactly one", opens.Load())
	}
}

func TestUnknownAppOpenExpiresWithoutResettingForEachMessage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	original := appProbeOutput
	t.Cleanup(func() { appProbeOutput = original })
	appProbeOutput = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("probe unavailable")
	}
	s := RealShower
	opens := 0
	s.Open = func([]string) error { opens++; return nil }
	s.Away = func() (bool, bool) { return true, true }
	s.Wait = func(time.Duration) { t.Fatal("unexpected presence wait") }
	wake := func() {
		s.ShowWhenIdle(ChatGPTOpenArgv("expiry-fixture"), "expiry-fixture", func(_, def bool, err error) {
			if def || err != nil {
				t.Fatalf("open result: deferred=%v err=%v", def, err)
			}
		})
	}
	wake()
	wake()
	if opens != 1 {
		t.Fatalf("a fresh message reset the unknown probe memo: opens=%d", opens)
	}
	key := sha256.Sum256([]byte("expiry-fixture"))
	path := filepath.Join(dir, "app-opens", hex.EncodeToString(key[:])+".json")
	// Age the receipt written by the production door; no setter can create
	// the memo for this test. Both sides of its fixed expiry are exercised.
	age := func(d time.Duration) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("production did not retain an open receipt:", err)
		}
		var receipt map[string]any
		if err := json.Unmarshal(raw, &receipt); err != nil {
			t.Fatal(err)
		}
		receipt["opened_at"] = time.Now().Add(-d).UTC().Format(time.RFC3339Nano)
		raw, err = json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	age(9 * time.Minute)
	wake()
	if opens != 1 {
		t.Fatalf("memo expired early: opens=%d", opens)
	}
	age(11 * time.Minute)
	wake()
	wake()
	if opens != 2 {
		t.Fatalf("forever-unknown thread not bounded and wakeable after expiry: opens=%d", opens)
	}
}

// Many app helpers are not the runtime holding a transcript. The production
// probe must exclude them before lsof, retaining the same overall deadline.
func TestOwnershipProbeQueriesOnlyBundledCodexRuntimes(t *testing.T) {
	original := appProbeOutput
	t.Cleanup(func() { appProbeOutput = original })
	appProbeOutput = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if _, bounded := ctx.Deadline(); !bounded {
			t.Fatal("unbounded ownership probe")
		}
		if binary == "/bin/ps" {
			stamp := ""
			if strings.Contains(strings.Join(args, " "), "lstart=") {
				stamp = "Mon Oct 5 06:01:00 2026 "
			}
			return []byte("101 " + stamp + chatGPTRoot + "Contents/MacOS/ChatGPT\n" +
				"102 " + stamp + chatGPTRoot + "helper\n" +
				"999 " + stamp + chatGPTRoot + "Contents/Resources/codex-cli/bin/codex\n"), nil
		}
		if binary != "/usr/sbin/lsof" || strings.Join(args, " ") != "-a -p 999 -Fn" {
			t.Errorf("helpers reached expensive ownership probe: %s %q", binary, args)
		}
		return []byte("p999\nn/tmp/rollout-runtime-fixture.jsonl\n"), nil
	}
	running, holds := ChatGPTHolds("runtime-fixture")
	if !running || !holds {
		t.Fatalf("setup/runtime ownership lost: running=%v holds=%v", running, holds)
	}
}

func TestAppOpenRearmsOnObservedUnloadButStillRateLimits(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	s := Shower{Holds: func(string) bool { return false }}
	opens := 0
	s.Open = func([]string) error { opens++; return nil }
	const thread = "unload-rate-fixture"
	wake := func() {
		s.ShowWhenIdle(ChatGPTOpenArgv(thread), thread, func(_, deferred bool, err error) {
			if deferred || err != nil {
				t.Fatalf("open: deferred=%v err=%v", deferred, err)
			}
		})
	}
	wake()
	wake()
	if opens != 1 {
		t.Fatalf("rapid known-unloaded observations repeated opening: %d", opens)
	}
	s.Holds = func(string) bool { return true }
	wake()
	s.Holds = func(string) bool { return false }
	wake()
	if opens != 1 {
		t.Fatalf("a loaded/unloaded transition bypassed the rate limit: %d", opens)
	}
	key := sha256.Sum256([]byte(thread))
	path := filepath.Join(dir, "app-opens", hex.EncodeToString(key[:])+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt["opened_at"] = time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339Nano)
	raw, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wake()
	wake()
	if opens != 2 {
		t.Fatalf("observed unload did not re-arm under the rate limit: %d", opens)
	}
}

func TestAppIncarnationRearmsAnOpenWithoutAProbeResult(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	original := appProbeOutput
	t.Cleanup(func() { appProbeOutput = original })
	stamp := "06:01:00"
	appProbeOutput = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary == "/bin/ps" {
			date := ""
			if strings.Contains(strings.Join(args, " "), "lstart=") {
				date = "Mon Oct 5 " + stamp + " 2026 "
			}
			return []byte("101 " + date + chatGPTRoot + "Contents/MacOS/ChatGPT\n" +
				"999 " + date + chatGPTRoot + "Contents/Resources/codex-cli/bin/codex\n"), nil
		}
		return nil, context.DeadlineExceeded // process epoch known, ownership unknown
	}
	s := RealShower
	opens := 0
	s.Open = func([]string) error { opens++; return nil }
	s.Away = func() (bool, bool) { return true, true }
	const thread = "incarnation-open-fixture"
	wake := func() {
		s.ShowWhenIdle(ChatGPTOpenArgv(thread), thread, func(_, deferred bool, err error) {
			if deferred || err != nil {
				t.Fatalf("open: deferred=%v err=%v", deferred, err)
			}
		})
	}
	wake()
	wake()
	if opens != 1 {
		t.Fatalf("same app incarnation repeated opening: %d", opens)
	}
	stamp = "06:02:00" // same PID, new start: actual process incarnation changed
	wake()
	if opens != 1 {
		t.Fatalf("new incarnation bypassed per-thread rate limit: %d", opens)
	}
	key := sha256.Sum256([]byte(thread))
	path := filepath.Join(dir, "app-opens", hex.EncodeToString(key[:])+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt["opened_at"] = time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339Nano)
	raw, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wake()
	wake()
	if opens != 2 {
		t.Fatalf("new incarnation failed to re-arm under rate limit: %d", opens)
	}
}

func TestCorruptAppOpenMemoRecoversAfterTheBoundedInterval(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	const thread = "corrupt-open-fixture"
	key := sha256.Sum256([]byte(thread))
	path := filepath.Join(dir, "app-opens", hex.EncodeToString(key[:])+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	opens := 0
	s := Shower{
		Holds: func(string) bool { return false },
		Open:  func([]string) error { opens++; return nil },
	}
	wake := func() {
		s.ShowWhenIdle(ChatGPTOpenArgv(thread), thread, func(_, deferred bool, err error) {
			if deferred || err != nil {
				t.Fatalf("open: deferred=%v err=%v", deferred, err)
			}
		})
	}
	wake()
	wake()
	if opens != 0 {
		t.Fatalf("corruption immediately re-armed opening: %d", opens)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal("garbage memo was not repaired:", err)
	}
	receipt["opened_at"] = time.Now().Add(-11 * time.Minute).UTC().Format(time.RFC3339Nano)
	raw, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	wake()
	wake()
	if opens != 1 {
		t.Fatalf("repaired memo did not restore a bounded future wake: %d", opens)
	}
}
