// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package codexipc

import (
	"errors"
	"net"
	"testing"
)

// Write is the production socket edge; an error may still report bytes sent.
type failedInputConn struct {
	net.Conn
	bytes int
}

func (c failedInputConn) Write([]byte) (int, error) {
	return c.bytes, errors.New("fixture write failed")
}

func TestNativeInputWriteCountsPartialBytesButNotDiscovery(t *testing.T) {
	for _, method := range []string{"initialize", "thread-owner-discovery", "thread-follower-start-turn", "thread-follower-steer-turn"} {
		for _, n := range []int{0, 1} {
			c := client{conn: failedInputConn{bytes: n}, id: "fixture"}
			_, err := c.call(method, 1, "owner", map[string]string{})
			if err == nil {
				t.Fatal("setup write did not fail")
			}
			want := n > 0 && (method == "thread-follower-start-turn" || method == "thread-follower-steer-turn")
			if c.inputWritten != want {
				t.Fatalf("%s bytes=%d: submitted=%v want=%v", method, n, c.inputWritten, want)
			}
			if BeforeInput(&deliveryError{err: err, submitted: c.inputWritten}) == want {
				t.Fatalf("%s bytes=%d: retry classification inverted", method, n)
			}
		}
	}
}
