package harnessenv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/paths"
)

// Keep RealShower's actual selector. Only the final native contact is fake;
// replacing Shower.Open here would let a dead helper route pass this guard.
func TestRealShowerRoutesChatGPTToNativeBackgroundMode(t *testing.T) {
	t.Setenv("DIBS_DIR", t.TempDir())
	t.Setenv("DIBS_TEST_FORBID_APP_OPEN", "1")
	previous := appOpenFixture
	t.Cleanup(func() { appOpenFixture = previous })
	calls := 0
	appOpenFixture = func(mode, url string, idle time.Duration) error {
		calls++
		if mode != "chatgpt-background" || url != "codex://threads/native-route" || idle != 0 {
			t.Fatalf("wrong production native mode: %q %q %v", mode, url, idle)
		}
		return nil
	}
	s := RealShower
	s.Holds = func(string) bool { return false }
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("production selector did not use the explicit fake native contact: %v", recovered)
		}
	}()
	opened, err := s.Show(ChatGPTOpenArgv("native-route"), "native-route")
	if err != nil || !opened || calls != 1 {
		t.Fatalf("production background route: opened=%v calls=%d err=%v", opened, calls, err)
	}
}

func TestBackgroundPairContentionNeverRecordsAnAttempt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	if err := os.MkdirAll(filepath.Join(dir, "app-opens"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app-opens", "background-pair.lock")
	previousCache := backgroundPairCacheDir
	backgroundPairCacheDir = func() (string, error) { return "", errors.New("fixture cache unavailable") }
	t.Cleanup(func() { backgroundPairCacheDir = previousCache })
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := paths.LockExclusive(f, false); err != nil {
		t.Fatal("setup lock:", err)
	}
	t.Cleanup(func() { paths.Unlock(f) })
	s := RealShower
	s.Holds = func(string) bool { return false }
	s.Open = func([]string) error { t.Fatal("contended pair opened app"); return nil }
	opened, err := s.Show(ChatGPTOpenArgv("contended-pair"), "contended-pair")
	if opened || !errors.Is(err, ErrAppOpenPairBusy) {
		t.Fatalf("contention: opened=%v err=%v", opened, err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "app-opens", "*.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("unattempted open recorded: %q %v", files, err)
	}
}

func TestBackgroundPairDefersAndOpensAfterRelease(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	if err := os.MkdirAll(filepath.Join(dir, "app-opens"), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "app-opens", "background-pair.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	previousCache := backgroundPairCacheDir
	backgroundPairCacheDir = func() (string, error) { return "", errors.New("fixture cache unavailable") }
	t.Cleanup(func() { backgroundPairCacheDir = previousCache })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := paths.LockExclusive(f, false); err != nil {
		t.Fatal("setup lock:", err)
	}
	t.Cleanup(func() { paths.Unlock(f) })
	s := RealShower
	s.Holds = func(string) bool { return false }
	opens := make(chan struct{}, 1)
	s.Open = func([]string) error { opens <- struct{}{}; return nil }
	s.Wait = func(time.Duration) { paths.Unlock(f) }
	type outcome struct {
		opened, deferred bool
		err              error
	}
	reports := make(chan outcome, 2)
	s.ShowWhenIdle(ChatGPTOpenArgv("deferred-pair"), "deferred-pair", func(opened, deferred bool, err error) {
		reports <- outcome{opened, deferred, err}
	})
	for _, wantDeferred := range []bool{true, false} {
		select {
		case got := <-reports:
			if got.err != nil || got.deferred != wantDeferred || got.opened == wantDeferred {
				t.Fatalf("deferred pair outcome: %+v", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("deferred pair did not finish after lock release")
		}
	}
	if len(opens) != 1 {
		t.Fatalf("pair opened %d times", len(opens))
	}
}

func TestBackgroundPairSerializesDifferentBoards(t *testing.T) {
	cache, firstDir, secondDir := t.TempDir(), t.TempDir(), t.TempDir()
	previousCache := backgroundPairCacheDir
	backgroundPairCacheDir = func() (string, error) { return cache, nil }
	t.Cleanup(func() { backgroundPairCacheDir = previousCache })
	t.Setenv("DIBS_DIR", firstDir)
	first, second := RealShower, RealShower
	first.Holds = func(string) bool { return false }
	second.Holds = first.Holds
	secondOpens := 0
	second.Open = func([]string) error { secondOpens++; return nil }
	first.Open = func([]string) error {
		t.Setenv("DIBS_DIR", secondDir)
		opened, err := second.Show(ChatGPTOpenArgv("second-board"), "second-board")
		if opened || !errors.Is(err, ErrAppOpenPairBusy) || secondOpens != 0 {
			t.Fatalf("different boards overlapped their desktop pair: opened=%v calls=%d err=%v", opened, secondOpens, err)
		}
		files, err := filepath.Glob(filepath.Join(secondDir, "app-opens", "*.json"))
		if err != nil || len(files) != 0 {
			t.Fatalf("unattempted second-board open recorded: %q %v", files, err)
		}
		return nil
	}
	if opened, err := first.Show(ChatGPTOpenArgv("first-board"), "first-board"); err != nil || !opened {
		t.Fatalf("first board open: opened=%v err=%v", opened, err)
	}
	if opened, err := second.Show(ChatGPTOpenArgv("second-board"), "second-board"); err != nil || !opened || secondOpens != 1 {
		t.Fatalf("second board after release: opened=%v calls=%d err=%v", opened, secondOpens, err)
	}
}

func TestUnavailableDesktopCachePreservesBoardWake(t *testing.T) {
	previousCache := backgroundPairCacheDir
	backgroundPairCacheDir = func() (string, error) { return "", errors.New("fixture cache unavailable") }
	t.Cleanup(func() { backgroundPairCacheDir = previousCache })
	t.Setenv("DIBS_DIR", t.TempDir())
	s := RealShower
	s.Holds = func(string) bool { return false }
	opens := 0
	s.Open = func([]string) error { opens++; return nil }
	if opened, err := s.Show(ChatGPTOpenArgv("cache-unavailable"), "cache-unavailable"); err != nil || !opened || opens != 1 {
		t.Fatalf("cache failure stranded board wake: opened=%v calls=%d err=%v", opened, opens, err)
	}
}
