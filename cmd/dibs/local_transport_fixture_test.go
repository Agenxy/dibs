// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/localtransport"
)

// A short, private profile is a kernel requirement on macOS, not a shortened
// timeout or a bypass of the production transport's ownership checks.
func cliIPCProfile(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dibs-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type localTestServer struct {
	*httptest.Server
	ipc  *http.Server
	once sync.Once
}

// Browser/TCP and native IPC exercise the same fixture handler, like dibd.
// Closing the fixture closes both, so outage tests still test a real outage.
func newLocalTestServer(t *testing.T, dir string, handler http.Handler) *localTestServer {
	t.Helper()
	result := &localTestServer{Server: httptest.NewServer(handler)}
	result.ipc = startFixtureIPC(t, dir, handler)
	t.Cleanup(result.Close)
	return result
}

func startFixtureIPC(t *testing.T, dir string, handler http.Handler) *http.Server {
	t.Helper()
	if localtransport.Supported() {
		ln, err := localtransport.Listen(dir)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: handler, ConnContext: localtransport.ConnContext, ReadHeaderTimeout: time.Second}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { _ = srv.Close(); _ = ln.Close() })
		return srv
	}
	return nil
}

func (s *localTestServer) Close() {
	s.once.Do(func() {
		if s.ipc != nil {
			_ = s.ipc.Close()
		}
		s.Server.Close()
	})
}
