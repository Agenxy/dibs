//go:build !linux

package liveness

import "time"

// processTimes is psTimes everywhere but Linux, where ps rounds processor
// time to whole seconds and /proc does not. Platform difference is a build
// tag concern, not a runtime guess from a parse result.
func processTimes(pid int) (cpu, elapsed time.Duration) { return psTimes(pid) }
