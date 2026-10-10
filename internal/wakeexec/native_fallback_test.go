// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package wakeexec

import (
	"errors"
	"testing"

	"github.com/agenxy/dibs/internal/harnessenv"
	"github.com/agenxy/dibs/internal/testcodexipc"
)

func TestNativePreInputFailureReleasesColdRoute(t *testing.T) {
	for _, mode := range []string{"changed", "unsupported", "fresh-error"} {
		t.Run(mode, func(t *testing.T) {
			s := testcodexipc.Start(t, mode, nil)
			f := Fields{Thread: testcodexipc.Thread, Agent: "worker", MsgType: "notify", Message: Compose("notify")}
			fresh := func() (string, error) {
				if mode == "fresh-error" {
					return "", errors.New("writer unavailable")
				}
				return ComposeNative(f), nil
			}
			out, handled := TryNative(harnessenv.ChatGPTApp, f,
				[]string{"codex", "queue", "--thread", f.Thread, "--message", f.Message}, fresh)
			if handled || out.OK || out.NoRetry || out.Disposition != "native_failed" || len(s.Inputs()) != 0 {
				t.Fatalf("pre-input failure stranded cold route: %+v handled=%v inputs=%d", out, handled, len(s.Inputs()))
			}
		})
	}
}
