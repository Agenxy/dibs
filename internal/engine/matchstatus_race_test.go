package engine

import (
	"encoding/json"
	"strconv"
	"sync"
	"testing"
)

// A status handed out is not a status that later shipments write into.
//
// MatchStatus returns a copy of the struct and releases the lock; the struct
// holds `Supplied`, a map, and NoteSuppliedIndex wrote into that same map in
// place. `/api/match-status` JSON-encodes the copy outside any lock, so a
// bridge shipping an index while doctor asked for status was a data race and,
// on a bad day, a fatal concurrent map access that takes the daemon down.
// Found by the pre-release review, round three; this is the race detector's
// case, and it fails on the previous commit under -race.
func TestAHandedOutStatusIsNotWrittenByLaterShipments(t *testing.T) {
	e := &Engine{}
	e.NoteSuppliedIndex("/repo/zero", "agent-0")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := range 50 {
				e.NoteSuppliedIndex("/repo/"+strconv.Itoa(i)+"/"+strconv.Itoa(j), "agent")
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				if _, err := json.Marshal(e.MatchStatus()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}
