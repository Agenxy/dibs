package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary doubles as notify-send, so the argv contract is exercised
// against a process rather than asserted from a string: what Dibs sends is
// what a real notify-send would receive, and what it prints back is what a
// real one prints (the chosen action key on stdout, nothing when dismissed).
func TestMain(m *testing.M) {
	if os.Getenv("DIBS_TEST_AS_NOTIFY_SEND") != "" {
		asNotifySend(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func asNotifySend(args []string) {
	if len(args) == 1 && args[0] == "--version" {
		_, _ = os.Stdout.WriteString("notify-send " + os.Getenv("DIBS_TEST_NOTIFY_VERSION") + "\n")
		return
	}
	// GetCapabilities, when this binary stands in for dbus-send.
	for _, a := range args {
		if strings.Contains(a, "GetCapabilities") {
			_, _ = os.Stdout.WriteString(os.Getenv("DIBS_TEST_DBUS_CAPS"))
			return
		}
	}
	// Record every invocation's argv for the test to inspect (appended, so a
	// second call cannot hide behind the first), then answer as configured.
	if f := os.Getenv("DIBS_TEST_NOTIFY_ARGV"); f != "" {
		if fh, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = fh.WriteString(strings.Join(args, "\n") + "\n---\n")
			_ = fh.Close()
		}
	}
	_, _ = os.Stdout.WriteString(os.Getenv("DIBS_TEST_NOTIFY_ANSWER"))
}

func stubNotifySend(t *testing.T, version string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	old, oldGoos, oldProbe := notifySend, goos, dbusProbe
	notifySend = func() string { return self }
	dbusProbe = func() (string, bool) { return self, false }
	goos = "linux"
	resetProbe()
	t.Cleanup(func() { notifySend, goos, dbusProbe = old, oldGoos, oldProbe; resetProbe() })
	t.Setenv("DIBS_TEST_DBUS_CAPS", `array [ string "actions" string "body" ]`)
	// The gate silences notifications for every test process (DIBS_NOTIFY=off),
	// which is right for the macOS helper and would make Reach answer
	// "switched off" before it ever looked for notify-send.
	t.Setenv(silenceEnv, "")
	t.Setenv("DIBS_TEST_AS_NOTIFY_SEND", "1")
	t.Setenv("DIBS_TEST_NOTIFY_VERSION", version)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
}

// On Linux a question is one notify-send with a button per label, and the
// answer comes back as the label pressed: the same contract Ask has on
// macOS, so the approval path behind it needs no platform branch. Issue #63.
func TestAskOnLinuxIsOneNotifySendWithButtons(t *testing.T) {
	stubNotifySend(t, "0.8.3")
	argv := filepath.Join(t.TempDir(), "argv")
	t.Setenv("DIBS_TEST_NOTIFY_ARGV", argv)
	t.Setenv("DIBS_TEST_NOTIFY_ANSWER", "2\n")

	// Through the PUBLIC Ask, so the routing is what is tested and not only
	// the Linux function behind it.
	got, err := Ask("Dibs", "worker asks for coordinator", "Deny", "Later", "Approve")
	if err != nil || got != "Approve" {
		t.Fatalf("Ask = %q, %v; want the label behind key 2", got, err)
	}
	sent, _ := os.ReadFile(argv)
	args := string(sent)
	if n := strings.Count(args, "\n---\n"); n != 1 {
		t.Errorf("notify-send was invoked %d times for one question, want exactly one", n)
	}
	for _, want := range []string{"--wait", "--action=0=Deny", "--action=1=Later", "--action=2=Approve", "--app-name=Dibs"} {
		if !strings.Contains(args, want) {
			t.Errorf("notify-send was not given %q:\n%s", want, args)
		}
	}
	if !strings.Contains(args, "--\nDibs\nworker asks for coordinator") {
		t.Errorf("title and body must follow `--`, so a body beginning with a dash is text:\n%s", args)
	}

	// Dismissed: no key on stdout, no error, no answer.
	t.Setenv("DIBS_TEST_NOTIFY_ANSWER", "")
	if got, err := Ask("Dibs", "x", "Approve"); err != nil || got != "" {
		t.Errorf("dismissed = %q, %v; want empty and nil", got, err)
	}
	// An answer that names no button is a failure, not a guess.
	t.Setenv("DIBS_TEST_NOTIFY_ANSWER", "7")
	if _, err := Ask("Dibs", "x", "Approve"); err == nil {
		t.Error("a key past the last button was accepted")
	}
	// The process's off switch holds here as it does on macOS: nothing is
	// posted, and Available says so.
	t.Setenv(silenceEnv, "off")
	resetProbe()
	before, _ := os.ReadFile(argv)
	if _, err := Ask("Dibs", "x", "Approve"); err == nil || Available() {
		t.Error("DIBS_NOTIFY=off did not stop the Linux route")
	}
	if after, _ := os.ReadFile(argv); string(after) != string(before) {
		t.Error("a silenced Ask still ran notify-send")
	}
}

// Each way the machine cannot ask is its own sentence, and an old libnotify
// that can show but not ask is refused rather than degraded to a banner
// nobody can answer.
func TestReachOnLinuxNamesWhatIsMissing(t *testing.T) {
	stubNotifySend(t, "0.8.3")
	if ok, why := linuxUsable(); !ok {
		t.Fatalf("usable notify-send reported unusable: %s", why)
	}

	t.Setenv("DIBS_TEST_NOTIFY_VERSION", "0.7.9")
	if ok, why := linuxUsable(); ok || !strings.Contains(why, "0.7.10") || !strings.Contains(why, "dibs web") {
		t.Errorf("libnotify 0.7.9: ok=%v why=%q; want refused, naming the version that can and the board", ok, why)
	}

	t.Setenv("DIBS_TEST_NOTIFY_VERSION", "0.8.3")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if ok, why := linuxUsable(); ok || !strings.Contains(why, "session bus") || !strings.Contains(why, "dibs web") {
		t.Errorf("headless: ok=%v why=%q; want the bus named and the board offered", ok, why)
	}

	// A daemon that cannot show buttons is refused as an approval path,
	// which is the issue's own requirement: actions matter, not just text.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	t.Setenv("DIBS_TEST_DBUS_CAPS", `array [ string "body" ]`)
	if ok, why := linuxUsable(); ok || !strings.Contains(why, "actions") || !strings.Contains(why, "dibs web") {
		t.Errorf("daemon without actions: ok=%v why=%q; want refused, naming the capability and the board", ok, why)
	}
	t.Setenv("DIBS_TEST_DBUS_CAPS", `array [ string "actions" ]`)
	oldProbe := dbusProbe
	dbusProbe = func() (string, bool) { return "", false }
	if ok, why := linuxUsable(); ok || !strings.Contains(why, "dbus-send") {
		t.Errorf("no probe tool: ok=%v why=%q; want 'unverified' with the tool to install", ok, why)
	}
	dbusProbe = oldProbe

	old := notifySend
	notifySend = func() string { return "" }
	t.Cleanup(func() { notifySend = old })
	if ok, why := linuxUsable(); ok || !strings.Contains(why, "libnotify") {
		t.Errorf("no notify-send: ok=%v why=%q; want the package named", ok, why)
	}
	// And Reach says the same thing through the public surface.
	if ok, why := Reach(); ok || !strings.Contains(why, "libnotify") {
		t.Errorf("Reach on Linux without notify-send: ok=%v why=%q", ok, why)
	}
}

func TestNotifySendVersionsParse(t *testing.T) {
	for in, want := range map[string][3]int{"0.8.3": {0, 8, 3}, "0.7.10": {0, 7, 10}, "0.8": {0, 8, 0}} {
		if got, err := parseVersion(in); err != nil || got != want {
			t.Errorf("parseVersion(%q) = %v, %v", in, got, err)
		}
	}
	if !atLeast([3]int{0, 7, 10}, minNotifySend) || atLeast([3]int{0, 7, 9}, minNotifySend) || !atLeast([3]int{1, 0, 0}, minNotifySend) {
		t.Error("atLeast is wrong about the boundary")
	}
}

// The engine asks Available() on its writer loop, so the Linux answer must be
// a cached bool and not a subprocess: one probe per process, however often
// it is asked, however slow the notification daemon. Found by the
// pre-release review, which read the five-second deadlines behind a call
// made while every agent waited.
func TestTheLinuxProbeRunsOncePerProcess(t *testing.T) {
	t.Setenv(silenceEnv, "")
	old, oldGoos, oldProbe := notifySend, goos, dbusProbe
	calls := 0
	notifySend = func() string { calls++; return "" }
	dbusProbe = func() (string, bool) { return "", false }
	goos = "linux"
	resetProbe()
	t.Cleanup(func() { notifySend, goos, dbusProbe = old, oldGoos, oldProbe; resetProbe() })

	for range 5 {
		_ = Available()
		_, _ = Reach()
	}
	if calls != 1 {
		t.Fatalf("the host was probed %d times across ten calls; the writer loop pays for every one", calls)
	}
}
