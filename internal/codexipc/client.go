// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package codexipc delivers into an existing desktop app, never starts a
// router, app-server or harness. This private protocol is measured against
// ChatGPT 26.1002.52244; incompatible replies fail, rather than guessing.
package codexipc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrNoOwner means the router cannot find an already loaded target.
var ErrNoOwner = errors.New("thread not loaded in the app; queued until opened")

// Receipt confirms app acceptance, not that the agent read its Dibs mailbox.
type Receipt struct {
	Disposition string
	TurnID      string
}

const (
	timeout  = 20 * time.Second // router owner discovery itself waits 10s
	maxFrame = 256 << 20        // installed app's uint32-LE protocol limit
	maxReply = 1 << 20          // matched metadata/turn receipts are small
)

type client struct {
	conn         net.Conn
	id           string
	inputWritten bool
}

type frame struct {
	Type              string          `json:"type"`
	RequestID         string          `json:"requestId,omitempty"`
	SourceClientID    string          `json:"sourceClientId,omitempty"`
	TargetClientID    string          `json:"targetClientId,omitempty"`
	TargetClientIDs   []string        `json:"targetClientIds,omitempty"`
	Version           int             `json:"version"`
	Method            string          `json:"method,omitempty"`
	Params            any             `json:"params,omitempty"`
	ResultType        string          `json:"resultType,omitempty"`
	HandledByClientID string          `json:"handledByClientId,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	Error             string          `json:"error,omitempty"`
	Response          any             `json:"response,omitempty"`
}

// Deliver resolves the socket afresh each time. A missing app socket is a cold
// target. Before-input failures are retryable; errors after an input write
// are UNKNOWN unless the selected owner explicitly refused it.
func Deliver(ctx context.Context, thread, text string) (Receipt, error) {
	return DeliverFresh(ctx, thread, func() (string, error) { return text, nil })
}

// ErrAppNotListening means the app's socket file exists and nothing accepts
// on it: the app is starting, stopping or restarting. No input was written.
// Measured 2026-10-10 on 26.1002.52244: a ChatGPT restart refused native
// dials for several seconds, and the cold route that followed opened two
// agents' threads in the person's window while the app came back.
var ErrAppNotListening = errors.New("the app's IPC socket refuses connections; the app is starting or stopping")

// DeliverFresh rechecks the board immediately before the one input write.
// Empty text means the identity closed or the original work was consumed.
func DeliverFresh(ctx context.Context, thread string, notice func() (string, error)) (receipt Receipt, err error) {
	var c *client
	defer func() {
		if err != nil && !errors.Is(err, ErrNoOwner) {
			err = &deliveryError{err: err, submitted: c != nil && c.inputWritten}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, closeConn, err := connect(ctx)
	if err != nil {
		return Receipt{}, err
	}
	defer closeConn()
	return c.deliver(thread, notice)
}

// ErrNoSocket is Owned's word for an app with no IPC socket at all, which is
// not evidence about any thread. Delivery calls the same case ErrNoOwner and
// takes the cold route.
var ErrNoSocket = errors.New("the app has no IPC socket")

// Owned reports whether the running app has thread loaded, through the same
// owner discovery delivery uses and with no input. ErrNoSocket,
// ErrAppNotListening and other connection errors are returned as they are:
// none of them is evidence that the thread is unloaded.
func Owned(ctx context.Context, thread string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, closeConn, err := connect(ctx)
	if err != nil {
		if errors.Is(err, ErrNoOwner) {
			return false, ErrNoSocket
		}
		return false, err
	}
	defer closeConn()
	_, err = c.owner(thread)
	if errors.Is(err, ErrNoOwner) {
		return false, nil
	}
	return err == nil, err
}

// connect resolves the socket afresh and initializes a client on it.
func connect(ctx context.Context) (*client, func(), error) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, err
		}
		home = filepath.Join(userHome, ".codex")
	}
	endpoint := filepath.Join(home, "ipc", "ipc.sock")
	if err := privateEndpoint(endpoint); err != nil {
		return nil, nil, err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", endpoint)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrNoOwner
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			return nil, nil, fmt.Errorf("native app connection: %w: %w", ErrAppNotListening, err)
		}
		return nil, nil, fmt.Errorf("native app connection: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	closeConn := func() { stop(); _ = conn.Close() }
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		closeConn()
		return nil, nil, err
	}
	c := &client{conn: conn, id: "initializing-client"}
	r, err := c.call("initialize", 0, "", map[string]string{"clientType": "dibs"})
	if err != nil {
		closeConn()
		return nil, nil, err
	}
	var init struct {
		ClientID string `json:"clientId"`
	}
	if json.Unmarshal(r.Result, &init) != nil || init.ClientID == "" {
		closeConn()
		return nil, nil, errors.New("native initialize response changed")
	}
	c.id = init.ClientID
	return c, closeConn, nil
}

func (c *client) write(f frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(b) > maxFrame {
		return errors.New("native frame exceeds bound")
	}
	header := make([]byte, 4)
	// #nosec G115 -- len(b) is checked against the 256 MiB protocol bound above.
	binary.LittleEndian.PutUint32(header, uint32(len(b)))
	n, err := io.Copy(c.conn, bytes.NewReader(append(header, b...)))
	if n > 0 && f.Type == "request" &&
		(f.Method == "thread-follower-start-turn" || f.Method == "thread-follower-steer-turn") {
		c.inputWritten = true
	}
	return err
}

func (c *client) read() (frame, error) {
	var size [4]byte
	if _, err := io.ReadFull(c.conn, size[:]); err != nil {
		return frame{}, err
	}
	n := binary.LittleEndian.Uint32(size[:])
	if n == 0 || n > maxFrame {
		return frame{}, fmt.Errorf("native frame length %d outside installed protocol limit %d", n, maxFrame)
	}
	if n > maxReply {
		// We never follow thread streams. An unsolicited full-history frame
		// carries no delivery receipt Dibs can use. Drain it without retaining
		// its bytes, preserving the next frame's boundary and the socket deadline.
		// A missing matched receipt still remains unknown; discarded data never
		// implies success or authorizes another input.
		_, err := io.CopyN(io.Discard, c.conn, int64(n))
		return frame{}, err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(c.conn, b); err != nil {
		return frame{}, err
	}
	var f frame
	if err := json.Unmarshal(b, &f); err != nil {
		return frame{}, fmt.Errorf("native JSON: %w", err)
	}
	return f, nil
}

func requestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
