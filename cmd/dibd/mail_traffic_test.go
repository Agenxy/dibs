// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
	"github.com/agenxy/dibs/internal/web"
)

func TestTrafficTotalsUseRealCoordinationGate(t *testing.T) {
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "traffic", box)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(core.NewState("traffic", core.DefaultLimits()), journal, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	})
	ui, err := web.New(e)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ui.Register(mux)
	gate := newAuthGate("traffic-secret", filepath.Join(t.TempDir(), "admin.hash"), "127.0.0.1:4777")
	for _, secret := range []string{"", "wrong", "traffic-secret"} {
		req := httptest.NewRequest("GET", "/api/traffic", nil)
		req.Header.Set("X-Dibs-Local", secret)
		out := httptest.NewRecorder()
		gate.wrap(mux).ServeHTTP(out, req)
		want := http.StatusUnauthorized
		if secret == "traffic-secret" {
			want = http.StatusOK
		}
		if out.Code != want {
			t.Fatalf("secret accepted=%v: %d %s", secret == "traffic-secret", out.Code, out.Body.String())
		}
		if want == http.StatusOK && !strings.Contains(out.Body.String(), `"days":[]`) {
			t.Fatal("counts handler not reached", out.Body.String())
		}
	}
}
