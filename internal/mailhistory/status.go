package mailhistory

// Status copies only scalar query boundaries. It never takes the compressed
// view lock, decodes a unit or retains any pointer into live canonical state.
type Status struct {
	InitialReady, Failed bool
	Generation           string
	Drained              Record
	OwnershipChange      uint64
}

// Status returns the current immutable scalar query boundaries.
func (i *Index) Status() Status {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return Status{
		InitialReady: i.initialReady, Failed: i.failed,
		Generation: i.generation, Drained: i.builtHead,
		OwnershipChange: i.lastOwnershipChange,
	}
}
