// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package testcodexipc is a framed, independently encoded desktop-app fixture.
// It contacts no installed app and records every native input it receives.
package testcodexipc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// Thread is the fixture agent identity.
const Thread = "7c3f0a11-2b44-4d90-9e57-1f2a3b4c5d6e"

// Server records requests from the real production protocol client.
type Server struct {
	Mode           string // idle, active, no-owner, changed, disconnect, refused, wrong-owner
	BeforeSnapshot func()
	mu             sync.Mutex
	inputs         []map[string]any
	errors         []error
}

// Start publishes an isolated private socket and removes it at test cleanup.
func Start(t *testing.T, mode string, before func()) *Server {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("measured desktop protocol uses a Unix socket")
	}
	home, err := os.MkdirTemp("", "di-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	t.Setenv("DIBS_TEST_NATIVE_IPC", "1")
	dir := filepath.Join(home, "ipc")
	if err = os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(dir, "ipc.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(dir, "ipc.sock"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{Mode: mode, BeforeSnapshot: before}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			s.serve(c)
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		<-done
		_ = os.RemoveAll(home)
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, err := range s.errors {
			t.Errorf("app fixture failed: %v", err)
		}
	})
	return s
}

// Inputs returns submitted native messages.
func (s *Server) Inputs() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.inputs...)
}

func (s *Server) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(25 * time.Second))
	for {
		f, err := read(c)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return
		}
		if err != nil {
			s.recordError(err)
			return
		}
		response := s.answer(f)
		if response == nil {
			continue
		}
		if s.Mode == "disconnect" && f["method"] == "thread-follower-start-turn" {
			return
		}
		if err = write(c, response); err != nil {
			s.recordError(err)
			return
		}
	}
}

func (s *Server) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors = append(s.errors, err)
}

func (s *Server) answer(f map[string]any) map[string]any {
	p, _ := f["params"].(map[string]any)
	r := map[string]any{
		"type": "response", "requestId": f["requestId"], "method": f["method"],
		"resultType": "success", "handledByClientId": "owner", "result": map[string]any{},
	}
	switch f["method"] {
	case "initialize":
		r["result"] = map[string]string{"clientId": "dibs-client"}
	case "thread-owner-discovery":
		if s.Mode == "no-owner" {
			r["resultType"], r["error"] = "error", "no-client-found"
		} else {
			r["result"] = map[string]bool{"supportsUntrustedAppInput": s.Mode != "unsupported"}
		}
	case "thread-stream-following-changed":
		if p["following"] != true {
			return nil
		}
		if s.BeforeSnapshot != nil {
			s.BeforeSnapshot()
		}
		status := "idle"
		if s.Mode == "active" {
			status = "active"
		}
		version := 11
		if s.Mode == "changed" {
			version = 12
		}
		return map[string]any{
			"type": "broadcast", "sourceClientId": "owner", "version": version,
			"method": "thread-stream-state-changed", "params": map[string]any{
				"hostId": "local", "conversationId": p["conversationId"], "change": map[string]any{
					"type": "snapshot", "conversationState": map[string]any{
						"id": p["conversationId"], "threadRuntimeStatus": map[string]string{"type": status},
						"turns": []any{map[string]string{"turnId": "prior-turn"}},
					},
				},
			},
		}
	case "thread-follower-start-turn", "thread-follower-steer-turn":
		s.mu.Lock()
		s.inputs = append(s.inputs, f)
		s.mu.Unlock()
		if s.Mode == "refused" {
			r["resultType"], r["error"] = "error", "refused"
		}
		if s.Mode == "wrong-owner" {
			r["handledByClientId"] = "somebody-else"
		}
		if f["method"] == "thread-follower-start-turn" {
			r["result"] = map[string]any{"result": map[string]any{
				"turn": map[string]string{"id": "new-turn", "status": "inProgress"},
			}}
		} else {
			r["result"] = map[string]any{"result": map[string]string{"turnId": "prior-turn"}}
		}
	default:
		return nil
	}
	return r
}

func read(r io.Reader) (map[string]any, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(header[:])
	if n == 0 || n > 1<<20 {
		return nil, errors.New("fixture frame outside bound")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	var f map[string]any
	err := json.Unmarshal(b, &f)
	return f, err
}

func write(w io.Writer, f map[string]any) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("fixture write exceeds bound")
	}
	header := make([]byte, 4)
	// #nosec G115 -- len(b) is checked against the 1 MiB fixture bound above.
	binary.LittleEndian.PutUint32(header, uint32(len(b)))
	_, err = w.Write(append(header, b...))
	return err
}
