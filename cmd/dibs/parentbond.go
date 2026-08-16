package main

import (
	"context"
	"os"
	"time"
)

// A bridge must not be able to outlive the harness that spawned it.
//
// One stdio bridge exists per harness session, so on a machine that opens and
// closes sessions all day, one that fails to exit is one orphan per session,
// each holding a subscription open against the daemon forever. That already
// happened once for a different reason (followStream reconnected with nothing
// able to stop it), and fixing that reason is not the same as making the class
// impossible.
//
// Closing stdin is the obvious signal and it is not sufficient. The bridge sees
// EOF when the LAST holder of the pipe's write end closes it, and the harness
// is not necessarily the last: a harness that also spawns shells hands each one
// the same descriptors, so a Claude Code that dies while a Bash tool is still
// running leaves the write end open and the bridge reading a pipe nobody will
// ever write to again. That is the orphan, and no amount of care inside the
// read loop prevents it, because the read loop is not what is wrong.
//
// So the lifetime is bound to the PROCESS instead of to the pipe, by the
// kernel, on both platforms Dibs supports:
//
//   - Linux: PR_SET_PDEATHSIG. The kernel sends SIGTERM when the parent exits.
//     It holds even if this process is wedged, which nothing in userspace can
//     promise.
//   - macOS: kqueue EVFILT_PROC/NOTE_EXIT. Event-driven, exact, no polling.
//
// Both have the same race: the parent can die between fork and the moment this
// registers. Both therefore re-check os.Getppid() afterwards, because a
// reparented process is an orphan by definition.

// parentGone returns a channel closed when the process that spawned this one
// exits. It never blocks the caller.
func parentGone(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	ppid := os.Getppid()
	// Already an orphan: spawned by something that has gone, or reparented
	// before we looked. 1 is init/launchd; 0 should not happen and is treated
	// the same way, because neither is a harness waiting for answers.
	if ppid <= 1 {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		watchParent(ctx, ppid)
	}()
	return done
}

// pollParent is the portable half, and the whole implementation where the OS
// offers nothing better. Reparenting is what "my parent died" looks like from
// inside a process, and it is not racy: getppid can only change once, in one
// direction, for this reason.
//
// Ten seconds because this is a backstop for the event-driven paths above, not
// the mechanism: an orphan lingering for a few seconds costs nothing, and a
// tighter loop would be syscalls burned on every bridge on the machine forever.
func pollParent(ctx context.Context, ppid int) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if os.Getppid() != ppid {
				return
			}
		}
	}
}
