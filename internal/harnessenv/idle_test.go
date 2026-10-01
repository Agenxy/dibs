package harnessenv

import (
	"sync"
	"testing"
	"time"
)

// fakeDesk is a person at a keyboard (idle times, in order) and the app.
type fakeDesk struct {
	mu     sync.Mutex
	idle   []time.Duration
	held   bool
	opened int
	done   chan struct{}
}

func (d *fakeDesk) shower(minIdle time.Duration) Shower {
	return Shower{
		Holds: func(string) bool { d.mu.Lock(); defer d.mu.Unlock(); return d.held },
		Open: func([]string) error {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.opened++
			return nil
		},
		Idle: func() (time.Duration, bool) {
			d.mu.Lock()
			defer d.mu.Unlock()
			if len(d.idle) == 0 {
				return time.Hour, true
			}
			v := d.idle[0]
			d.idle = d.idle[1:]
			return v, true
		},
		MinIdle: minIdle,
		Wait:    func(time.Duration) {},
		Poll:    time.Millisecond,
	}
}

func (d *fakeDesk) report(t *testing.T) func(opened, deferred bool, err error) {
	return func(opened, deferred bool, err error) {
		if err != nil {
			t.Errorf("open failed: %v", err)
		}
		if !deferred {
			close(d.done)
		}
	}
}

func (d *fakeDesk) wait(t *testing.T) {
	t.Helper()
	select {
	case <-d.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the deferred open never finished")
	}
}

var argv = []string{"/usr/bin/open", "codex://threads/x"}

// The operator asked that a wake not disrupt them, and opening a thread brings
// the app to the front whatever is passed (measured: `open -g` and handing
// focus back both lost). So an open waits until the person has been idle.
func TestAThreadIsOpenedOnlyOnceThePersonIsIdle(t *testing.T) {
	d := &fakeDesk{idle: []time.Duration{5 * time.Second, 30 * time.Second, 3 * time.Minute}, done: make(chan struct{})}
	s := d.shower(2 * time.Minute)
	deferred := false
	s.ShowWhenIdle(argv, "t1", func(opened, def bool, err error) {
		if def {
			deferred = true
			if opened {
				t.Error("reported opened while the person was active")
			}
			return
		}
		d.report(t)(opened, def, err)
	})
	if !deferred {
		t.Fatal("an active person was interrupted: the app was opened without waiting")
	}
	d.wait(t)
	if d.opened != 1 {
		t.Errorf("opened %d times once the person went idle, want once", d.opened)
	}
}

// And the cases where nothing should wait, or nothing should open.
func TestTheIdleGateNeverLosesAnOpenOrMakesASecondOne(t *testing.T) {
	t.Run("idle already: opened at once", func(t *testing.T) {
		d := &fakeDesk{idle: []time.Duration{10 * time.Minute}, done: make(chan struct{})}
		d.shower(2*time.Minute).ShowWhenIdle(argv, "t2", d.report(t))
		d.wait(t)
		if d.opened != 1 {
			t.Errorf("opened %d times", d.opened)
		}
	})
	t.Run("zero means at once, whatever the person is doing", func(t *testing.T) {
		d := &fakeDesk{idle: []time.Duration{time.Second}, done: make(chan struct{})}
		d.shower(0).ShowWhenIdle(argv, "t3", d.report(t))
		d.wait(t)
		if d.opened != 1 {
			t.Errorf("opened %d times with open_app_after_idle = 0", d.opened)
		}
	})
	t.Run("an unreadable idle time does not wait", func(t *testing.T) {
		d := &fakeDesk{done: make(chan struct{})}
		s := d.shower(2 * time.Minute)
		s.Idle = func() (time.Duration, bool) { return 0, false }
		s.ShowWhenIdle(argv, "t4", d.report(t))
		d.wait(t)
		if d.opened != 1 {
			t.Error("an unreadable idle time held the open forever")
		}
	})
	t.Run("loaded meanwhile: nothing to open", func(t *testing.T) {
		d := &fakeDesk{idle: []time.Duration{time.Second}, done: make(chan struct{})}
		s := d.shower(2 * time.Minute)
		checked := make(chan struct{})
		s.Wait = func(time.Duration) { d.mu.Lock(); d.held = true; d.mu.Unlock() }
		holds := s.Holds
		s.Holds = func(th string) bool {
			h := holds(th)
			if h {
				defer close(checked) // the waiter saw it loaded and gives up
			}
			return h
		}
		s.ShowWhenIdle(argv, "t5", func(bool, bool, error) {})
		select {
		case <-checked:
		case <-time.After(5 * time.Second):
			t.Fatal("the waiter never looked again")
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.opened != 0 {
			t.Error("opened a thread the app had loaded while the open waited: the app jumps forward for nothing")
		}
	})
	t.Run("a second wake does not start a second waiter", func(t *testing.T) {
		gate := make(chan struct{})
		d := &fakeDesk{idle: []time.Duration{time.Second, time.Second}, done: make(chan struct{})}
		s := d.shower(2 * time.Minute)
		s.Wait = func(time.Duration) { <-gate }
		calls := 0
		s.ShowWhenIdle(argv, "t6", func(_, def bool, _ error) {
			if def {
				calls++
			} else {
				close(d.done)
			}
		})
		s.ShowWhenIdle(argv, "t6", func(_, def bool, _ error) {
			if def {
				calls++
			}
		})
		close(gate)
		d.wait(t)
		if calls != 1 || d.opened != 1 {
			t.Errorf("two wakes for one thread: %d waiters, %d opens", calls, d.opened)
		}
	})
}
