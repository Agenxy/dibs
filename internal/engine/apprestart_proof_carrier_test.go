package engine

import (
	"sync/atomic"

	"github.com/agenxy/dibs/internal/wakeexec"
)

// Compile-only carriers for the identical feature-branch test. Old Engine.Run
// does not read either one: they must not add restart behavior to main.
var appRestartEpoch = func() *atomic.Value {
	v := new(atomic.Value)
	v.Store(func() (string, bool) { return "", false })
	return v
}()

var restartQueue = func([]string, string, string) wakeexec.RestartQueueOutcome {
	return wakeexec.RestartQueueOutcome{}
}
