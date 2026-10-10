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
	"strings"
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
	follows        int
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

// Follows counts stream-follow requests, which fetch full thread history.
func (s *Server) Follows() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.follows
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
		if strings.HasPrefix(s.Mode, "large-") && f["method"] == "thread-owner-discovery" {
			snapshot := s.answer(map[string]any{
				"method": "thread-stream-following-changed",
				"params": map[string]any{"following": true, "conversationId": Thread},
			})
			// Injection is unsolicited and must not count as a real follow.
			s.mu.Lock()
			s.follows--
			s.mu.Unlock()
			if err = write(c, snapshot); err != nil {
				s.recordError(err)
				return
			}
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
		s.ownerResult(r)
	case "thread-stream-following-changed":
		s.mu.Lock()
		s.follows++
		s.mu.Unlock()
		if p["following"] != true {
			return nil
		}
		return s.snapshot(p["conversationId"])
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
			turn := "new-turn"
			if s.Mode == "active" || s.Mode == "large-active" {
				turn = "prior-turn"
			}
			r["result"] = map[string]any{"result": map[string]any{
				"turn": map[string]string{"id": turn, "status": "inProgress"},
			}}
		} else {
			r["result"] = map[string]any{"result": map[string]string{"turnId": "prior-turn"}}
		}
	default:
		return nil
	}
	return r
}

func (s *Server) ownerResult(r map[string]any) {
	if s.BeforeSnapshot != nil {
		s.BeforeSnapshot() // historical fixture seam: before the input freshness fence
	}
	if s.Mode == "no-owner" {
		r["resultType"], r["error"] = "error", "no-client-found"
		return
	}
	r["result"] = map[string]bool{"supportsUntrustedAppInput": s.Mode != "unsupported"}
	if s.Mode == "changed" {
		r["result"] = map[string]string{"changed": "owner metadata"}
	}
}

func (s *Server) snapshot(conversation any) map[string]any {
	status := "idle"
	if s.Mode == "active" || s.Mode == "large-active" {
		status = "active"
	}
	version := 11
	if s.Mode == "changed" {
		version = 12
	}
	turns := []any{map[string]string{"turnId": "prior-turn"}}
	if strings.HasPrefix(s.Mode, "large-") {
		turns = []any{map[string]any{"turnId": "older-turn", "items": []any{
			map[string]string{"type": "agentMessage", "text": strings.Repeat("history ", 1_310_720)},
		}}, map[string]string{"turnId": "prior-turn"}}
	}
	return map[string]any{
		"type": "broadcast", "sourceClientId": "owner", "version": version,
		"method": "thread-stream-state-changed", "params": map[string]any{
			"hostId": "local", "conversationId": conversation, "change": map[string]any{
				"type": "snapshot", "conversationState": map[string]any{
					"id": conversation, "threadRuntimeStatus": map[string]string{"type": status}, "turns": turns,
				},
			},
		},
	}
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
	if len(b) > 16<<20 {
		return errors.New("fixture write exceeds bound")
	}
	header := make([]byte, 4)
	// #nosec G115 -- len(b) is checked against the 16 MiB fixture bound above.
	binary.LittleEndian.PutUint32(header, uint32(len(b)))
	_, err = w.Write(append(header, b...))
	return err
}
