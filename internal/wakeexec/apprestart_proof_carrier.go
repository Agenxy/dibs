package wakeexec

// RestartQueueOutcome exists here only to compile the identical headline test
// against pre-feature main. No old production path constructs or reads it.
type RestartQueueOutcome struct {
	OK, Retryable bool
}
