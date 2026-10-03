package main

import (
	"context"
	"fmt"
	"html"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Enter through restartUnit and the actual service manager. A fake argv
// recorder cannot prove that bootstrap already starts a RunAtLoad service,
// which is the condition that made kickstart -k kill its replacement.
func TestReloadedLaunchdServiceStartsExactlyOnce(t *testing.T) {
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	if err := exec.Command("launchctl", "print", domain).Run(); err != nil {
		t.Skip("this Mac has no user GUI launchd domain")
	}
	for _, auto := range []bool{true, false} {
		t.Run(fmt.Sprintf("RunAtLoad=%t", auto), func(t *testing.T) {
			testSingleLaunchdStart(t, domain, auto)
		})
	}
}

func TestLaunchdSingleStartFixture(_ *testing.T) {
	if os.Getenv("DIBS_TEST_SERVICE_FIXTURE") != "1" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
}

func testSingleLaunchdStart(t *testing.T, domain string, auto bool) {
	t.Helper()
	label := fmt.Sprintf("org.agenxy.dibs.test-single-start.%d.%d", os.Getpid(), time.Now().UnixNano())
	dir := t.TempDir()
	unit := filepath.Join(dir, label+".plist")
	target := domain + "/" + label
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>-test.run=^TestLaunchdSingleStartFixture$</string></array>
<key>EnvironmentVariables</key><dict><key>DIBS_TEST_SERVICE_FIXTURE</key><string>1</string></dict>
<key>RunAtLoad</key><%t/>
<key>KeepAlive</key><false/>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>`, label, html.EscapeString(os.Args[0]), auto,
		html.EscapeString(filepath.Join(dir, "out.log")), html.EscapeString(filepath.Join(dir, "err.log")))
	if err := os.WriteFile(unit, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("launchctl", "bootout", target).Run() })
	start := time.Now()
	if err := restartUnit(unit); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	out, err := exec.Command("launchctl", "print", target).Output()
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`(?m)^\s*runs = (\d+)\s*$`).FindSubmatch(out)
	if len(match) != 2 {
		t.Fatal("launchd did not report how many times it spawned the fixture")
	}
	runs, err := strconv.Atoi(string(match[1]))
	if err != nil || runs != 1 {
		t.Fatalf("restart spawned %d times, want one (elapsed %s): bootstrap's replacement was killed", runs, elapsed)
	}
	t.Logf("RunAtLoad=%t: one service start in %s", auto, elapsed)
}
