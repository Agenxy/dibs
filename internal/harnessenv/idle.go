package harnessenv

import (
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Opening a thread in the app brings the app to the FRONT, and nothing a
// caller passes prevents it. Measured 2026-10-01: `open -g` (do not activate)
// still put ChatGPT in front within 250ms, and handing focus back to the
// previous app lost the race three times in six seconds as the thread loaded.
// The operator asked that a wake not disrupt them. So when a thread has to be
// opened, Dibs waits until the person has been away from the keyboard and
// mouse for a while, and opens it then. Only the first wake per thread per app
// run gets here at all: a loaded thread stays loaded, and the queue delivers
// to it silently.

// DefaultOpenAfterIdle is how long the person must have been idle before a
// thread is opened in the app: `[wake] open_app_after_idle`. Zero opens at once.
const DefaultOpenAfterIdle = 2 * time.Minute

// maxIdleWait bounds how long one deferred open keeps asking. A day: past it
// the message is still queued in the app and is delivered whenever the thread
// is next opened, so giving up loses nothing but the open.
const maxIdleWait = 24 * time.Hour

var hidIdle = regexp.MustCompile(`"HIDIdleTime" = (\d+)`)

// UserIdle is how long since the person last used the keyboard or mouse, from
// IOKit's HID system, which needs no permission to read. ok is false when it
// cannot be read (not macOS, ioreg missing): the caller then does not wait,
// because an unreadable idle time is not evidence that anybody is there.
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

// ShowWhenIdle is Show, deferred until the person has been idle for
// s.MinIdle. It never blocks the caller: a wait runs on its own goroutine.
// report is told what happened, once, for the caller's log.
func (s Shower) ShowWhenIdle(argv []string, thread string, report func(opened, deferred bool, err error)) {
	if len(argv) == 0 || s.Holds(thread) {
		return
	}
	if s.ready() {
		opened, err := s.Show(argv, thread)
		report(opened, false, err)
		return
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
			if s.Holds(thread) {
				return // somebody opened it, or the app loaded it: nothing to do
			}
			if s.ready() {
				opened, err := s.Show(argv, thread)
				report(opened, false, err)
				return
			}
		}
	}()
}

// ready reports whether opening now would not interrupt a person.
func (s Shower) ready() bool {
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
