// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package engine

import (
	"testing"

	"github.com/agenxy/dibs/internal/wakeexec"
)

// A refused socket beside a running app was read as "restarting" for as long
// as it lasted, and ChatGPT 26.1117.31558 does not open the IPC router at all:
// every ChatGPT agent's mail was held "until IPC is ready" with no end, on a
// build that will never be ready. Restarting is a window, not a state. It
// lasts until afterAppReturn settles that app incarnation, whether or not IPC
// answered; after that a refused socket is the build, and mail takes the
// background queue route, which never navigates.
func TestRefusedSocketHoldsMailOnlyUntilTheReturnSettles(t *testing.T) {
	prior := appRestartEpoch.Load()
	appRestartEpoch.Store(func() (string, bool) { return "app:B", true })
	t.Cleanup(func() { appRestartEpoch.Store(prior) })

	refused := wakeexec.NativeOutcome{Disposition: "native_failed", AppNotListening: true}
	e := &Engine{}

	e.appSettled.Store("app:A") // the previous incarnation; app:B is still coming up
	if !e.heldForAppReturn(wakePlan{}, "worker", refused) {
		t.Fatal("a refused socket during the return of a new app incarnation must hold the mail")
	}

	e.appSettled.Store("app:B") // the return settled, and the socket still refuses
	if e.heldForAppReturn(wakePlan{}, "worker", refused) {
		t.Fatal("after the return settled, a refused socket is this app build; holding it is holding forever")
	}
}
