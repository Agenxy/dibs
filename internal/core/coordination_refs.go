package core

import (
	"strconv"
	"strings"
)

// Coordination is proven structurally, never inferred from message prose.
// Every shared ref must be explained; one unrelated shared objective still
// deserves its duplicate warning. A shared wait survives mail retention.
func (s *State) coordinationRefs(a, b string, refs []string, aWaiting, bWaiting string) bool {
	if a == "" || b == "" || len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		kind, serial, ok := coordinationRef(ref)
		if !ok {
			return false
		}
		if aWaiting != "" && bWaiting != "" {
			continue
		}
		m := s.Messages[serial]
		if m == nil || m.Type != kind {
			return false
		}
		paired := (m.From == a && m.To == b) || (m.From == b && m.To == a)
		if !paired {
			return false
		}
	}
	return true
}

func sharedWaitingRefs(a, b Slot) bool {
	if a.Waiting == "" || b.Waiting == "" {
		return false
	}
	shared := sharedStrings(a.Refs, b.Refs)
	if len(shared) == 0 {
		return false
	}
	for _, ref := range shared {
		if _, _, ok := coordinationRef(ref); !ok {
			return false
		}
	}
	return true
}

func coordinationRef(ref string) (string, uint64, bool) {
	kind, id, _ := strings.Cut(normRef(ref), ":")
	if kind != MsgRequest && kind != MsgQuestion {
		return "", 0, false
	}
	serial, err := strconv.ParseUint(id, 10, 64)
	return kind, serial, err == nil && serial > 0
}
