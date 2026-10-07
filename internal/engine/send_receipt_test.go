// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"sync"
	"testing"
)

func TestSendAttemptDeadlineAndWriterAdmissionAreExclusive(t *testing.T) {
	for range 1000 {
		attempt := &SendAttempt{}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var abandoned, admitted bool
		go func() {
			defer wg.Done()
			<-start
			abandoned = attempt.AbandonBeforeWriter()
		}()
		go func() {
			defer wg.Done()
			<-start
			admitted = attempt.admitToWriter()
		}()
		close(start)
		wg.Wait()
		if abandoned == admitted {
			t.Fatalf("deadline and writer disagreed: abandoned=%t admitted=%t", abandoned, admitted)
		}
		if abandoned && attempt.maySubmit() {
			t.Fatal("abandoned attempt was allowed to enqueue late")
		}
	}
}
