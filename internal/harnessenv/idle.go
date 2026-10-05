package harnessenv

import (
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/agenxy/dibs/internal/notify"
)

// Claude closed-session recovery waits for positive lock/display-sleep evidence
// or the configured HID idle interval. ChatGPT queued mail takes the prompt,
// bounded background path in app_open.go and does not consult this gate.

// DefaultOpenAfterIdle is Claude recovery's AFK fallback:
// `[wake] open_app_after_idle`. Lock or sleeping displays qualify sooner.
const DefaultOpenAfterIdle = 10 * time.Minute

// maxIdleWait bounds how long one deferred open keeps asking. A day: past it
// the message is still queued in the app and is delivered whenever the thread
// is next opened, so giving up loses nothing but the open.
const maxIdleWait = 24 * time.Hour

var hidIdle = regexp.MustCompile(`"HIDIdleTime" = (\d+)`)

// UserIdle is how long since the person last used the keyboard or mouse, from
// IOKit's HID system, which needs no permission to read. ok is false when it
// cannot be read (not macOS, ioreg missing). Production then leaves the open
// queued unless another known away signal qualifies.
func UserIdle() (time.Duration, bool) {
	out, err := exec.Command("/usr/sbin/ioreg", "-c", "IOHIDSystem", "-d", "4").Output()
	if err != nil {
		return 0, false
	}
	m := hidIdle.FindSubmatch(out)
	if m == nil {
		return 0, false
	}
	ns, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Duration(ns), true
}

// pendingOpens are the threads with an open already waiting for idle, so a
// second wake for the same thread does not start a second waiter.
var pendingOpens = struct {
	sync.Mutex
	threads map[string]bool
}{threads: map[string]bool{}}

// PendingAppOpen reports a local, derived waiter. Losing it leaves the notice
// in the app queue; this is a display diagnostic, never coordination truth.
func PendingAppOpen(thread string) bool {
	pendingOpens.Lock()
	defer pendingOpens.Unlock()
	return pendingOpens.threads[thread]
}

// ShowWhenIdle is Show, deferred until the person has been idle for
// s.MinIdle. It never blocks the caller: a wait runs on its own goroutine.
// report is told what happened, once, for the caller's log.
func (s Shower) ShowWhenIdle(argv []string, thread string, report func(opened, deferred bool, err error)) {
	// ChatGPT mail is wakeable whatever the person is doing. Claude's closed
	// session recovery retains its separate away policy.
	if isChatGPTOpen(argv) {
		opened, err := s.showChatGPT(argv, thread)
		report(opened, false, err)
		return
	}
	if len(argv) == 0 || s.holds(thread) {
		return
	}
	if s.ready() {
		opened, err := s.Show(argv, thread)
		if !errors.Is(err, notify.ErrNotAway) {
			report(opened, false, err)
			return // success or a permanent helper failure; mail stays queued
		}

	}
	pendingOpens.Lock()
	if pendingOpens.threads[thread] {
		pendingOpens.Unlock()
		return
	}
	pendingOpens.threads[thread] = true
	pendingOpens.Unlock()
	report(false, true, nil)
	go func() {
		defer func() {
			pendingOpens.Lock()
			delete(pendingOpens.threads, thread)
			pendingOpens.Unlock()
		}()
		for waited := time.Duration(0); waited < maxIdleWait; waited += s.poll() {
			s.Wait(s.poll())
			if s.holds(thread) {
				return // somebody opened it, or the app loaded it: nothing to do
			}
			if s.ready() {
				opened, err := s.Show(argv, thread)
				if !errors.Is(err, notify.ErrNotAway) {
					report(opened, false, err)
					return
				}

			}
		}
	}()
}

// ready reports whether opening now would not interrupt a person.
func (s Shower) ready() bool {
	if s.Away != nil {
		away, known := s.Away()
		if known && away {
			return true
		}
		if s.Idle == nil {
			return false
		}
		idle, idleKnown := s.Idle()
		return idleKnown && idle >= s.MinIdle
	}
	if s.MinIdle <= 0 || s.Idle == nil {
		return true
	}
	idle, ok := s.Idle()
	return !ok || idle >= s.MinIdle
}

func (s Shower) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return time.Minute
}
