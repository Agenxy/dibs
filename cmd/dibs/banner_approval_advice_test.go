// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/mcp"
	"github.com/agenxy/dibs/internal/notify"
	"github.com/agenxy/dibs/internal/web"
)

// No notification is posted by the fixture. Settings still enter through the
// actual installed-helper lookup and a private --status subprocess, not a flag
// or setter inside the code whose presentation is being tested.
type bannerAdviceDesktop struct{}

func (bannerAdviceDesktop) Available() bool { return true }
func (bannerAdviceDesktop) Ask(humanask.Message) (humanask.Answer, error) {
	return humanask.Answer{}, nil
}

func TestBannerAdviceThroughNativeStatusAndActualSurfaces(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS notification settings")
	}
	if os.Getenv("DIBS_BANNER_ADVICE_DRIVER") == "1" {
		runBannerAdviceSurfaces(t)
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	driver := filepath.Join(dir, "banner-advice-driver")
	helper := filepath.Join(dir, "Dibs.app", "Contents", "MacOS", "dibs-notify")
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{driver, helper} {
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, driver, "-test.run=^TestBannerAdviceThroughNativeStatusAndActualSurfaces$") // #nosec G204 -- private copy of this test binary
	cmd.Env = append(os.Environ(), "DIBS_BANNER_ADVICE_DRIVER=1", "DIBS_NOTIFY=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native status and presentation driver: %v\n%s", err, out)
	}
}

func runBannerAdviceSurfaces(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DIBS_DIR", t.TempDir())
	t.Setenv("DIBS_SETTINGS_HELPER_MODE", "doctor")
	t.Setenv("DIBS_SETTINGS_GUARD_MODE", "settings")
	marker := filepath.Join(t.TempDir(), "status-probed")
	t.Setenv("DIBS_SETTINGS_HELPER_MARKER", marker)
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "banner-advice", box)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(core.NewState("banner-advice", core.DefaultLimits()), led, nil)
	e.SetHumanNotifier(bannerAdviceDesktop{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; _ = led.Close() })
	api := httptest.NewServer(mcp.New(e))
	t.Cleanup(api.Close)
	f := &recoveryStdio{srv: api}
	registered := f.direct(t, "register", map[string]any{"name": "sender", "nonce": "banner-advice-sender"})
	token, ok := registered["token"].(string)
	if !ok || token == "" {
		t.Fatalf("setup registration: %v", registered)
	}
	if r := f.direct(t, "check_in", map[string]any{"token": token}); r["error"] != nil || r["code"] != nil {
		t.Fatalf("setup checkpoint: %v", r)
	}
	w, err := web.New(e)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	w.Register(mux)
	board := httptest.NewServer(mux)
	t.Cleanup(board.Close)
	const advice = "Dibs approval requests appear as banners and vanish. Set System Settings > Notifications > Dibs > Alert style: Alerts."
	for _, style := range []string{"banner", "unknown", "none", "alert"} {
		t.Run(style, func(t *testing.T) {
			settings, err := json.Marshal(map[string]any{
				"version": 1, "authorization_status": "authorized", "alert_style": style,
				"alert_setting": "enabled", "notification_center_setting": "enabled", "lock_screen_setting": "enabled",
				"time_sensitive_setting": "not-supported", "focus": map[string]any{"authorization": "not-determined", "observable": false},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("DIBS_SETTINGS_GUARD_VALUE", string(settings))
			// The daemon startup calls this same production reach probe.
			if ok, _ := notify.Reach(); !ok {
				t.Fatal("setup native reach probe refused the fixture")
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "status" {
				t.Fatalf("actual native status helper did not execute: %s %v", data, err)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			sent := f.direct(t, "send", map[string]any{"token": token, "to": "human", "type": "request", "body": "private approval fixture"})
			if sent["msg_serial"] == nil || sent["human_route"] != "desktop" {
				t.Fatal("setup human send did not reach the desktop route")
			}
			if has := strings.Contains(sent["notify_hint"].(string), advice); has != (style == "banner") {
				t.Errorf("sender guidance banner=%t: %q", style == "banner", sent["notify_hint"])
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("send ran a native status probe instead of returning immediately")
			}
			// Rebuilding a view cannot erase a warning already measured by the
			// notifier, even if this new view's first observation is unknown.
			fresh, err := web.New(e)
			if err != nil {
				t.Fatal(err)
			}
			freshMux := http.NewServeMux()
			fresh.Register(freshMux)
			freshBoard := httptest.NewServer(freshMux)
			t.Cleanup(freshBoard.Close)
			resp, err := freshBoard.Client().Get(freshBoard.URL + "/")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("actual board page: %v %d", err, resp.StatusCode)
			}
			page := html.UnescapeString(string(body))
			_, part, found := strings.Cut(page, `<aside id="notification-advice"`)
			if !found {
				t.Fatal("actual board has no persistent notification advice element")
			}
			tag, part, _ := strings.Cut(part, ">")
			text, _, _ := strings.Cut(part, "</aside>")
			want := style != "alert" // unknown must not erase an observed warning
			if strings.Contains(tag, "hidden") == want || strings.Contains(text, advice) != want {
				t.Errorf("persistent board warning visible=%t; tag=%q text=%q", want, tag, text)
			}
		})
	}
}
