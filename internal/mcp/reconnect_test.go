package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/harnessenv"
)

// Enter through stateless discovery, not observeBridge by hand: process
// observation must not serialize otherwise independent first MCP requests.
func TestConcurrentBridgeDiscoveryDoesNotWaitForAnotherProbe(t *testing.T) {
	srv, _ := newServer(t)
	s := srv.Config.Handler.(*Server)
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	s.bridges.probe = func(pid int, _ string) (harnessenv.AppIncarnation, bool, error) {
		switch pid {
		case 101:
			close(firstEntered)
			<-releaseFirst
		case 102:
			close(secondEntered)
		}
		return harnessenv.AppIncarnation{}, false, nil
	}
	// Register cleanup after server cleanup, so a failing assertion releases
	// the blocked handler before httptest.Server waits for its connections.
	t.Cleanup(release)
	discover := func(pid int) <-chan error {
		done := make(chan error, 1)
		go func() {
			body, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": pid, "method": "server/discover",
				"params": map[string]any{"_meta": map[string]any{
					BridgePIDMetaKey: pid, BridgeStartMetaKey: "Sun Oct 4 06:00:00 2026",
					HostMetaKey: s.eng.HostID(),
				}},
			})
			if err != nil {
				done <- err
				return
			}
			req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
			if err != nil {
				done <- err
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("MCP-Protocol-Version", "2026-07-28")
			resp, err := srv.Client().Do(req)
			if err != nil {
				done <- err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			var out struct {
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err = json.NewDecoder(resp.Body).Decode(&out); err == nil && (resp.StatusCode != http.StatusOK || len(out.Result) == 0 || len(out.Error) != 0) {
				err = fmt.Errorf("discovery did not succeed: HTTP%d result=%s error=%s", resp.StatusCode, out.Result, out.Error)
			}
			done <- err
		}()
		return done
	}
	first := discover(101)
	select {
	case <-firstEntered:
	case err := <-first:
		t.Fatalf("setup: first discovery never reached probe: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("setup: first discovery never reached probe")
	}
	duplicate := discover(101)
	select {
	case err := <-duplicate:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("an in-flight incarnation started a duplicate probe or blocked its MCP request")
	}
	second := discover(102)
	select {
	case <-secondEntered:
	case err := <-second:
		t.Fatalf("second discovery bypassed the probe: %v", err)
	case <-time.After(time.Second):
		t.Fatal("second first request waited behind an unrelated bridge probe")
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second first request did not complete while the first probe was blocked")
	}
	release()
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("the released first probe did not complete discovery")
	}
}

func TestBridgeDiscoveryRetriesFailedProbeAndCachesCompletedProbe(t *testing.T) {
	srv, _ := newServer(t)
	s := srv.Config.Handler.(*Server)
	var probes atomic.Int32
	s.bridges.probe = func(int, string) (harnessenv.AppIncarnation, bool, error) {
		if probes.Add(1) == 1 {
			return harnessenv.AppIncarnation{}, false, os.ErrDeadlineExceeded
		}
		return harnessenv.AppIncarnation{}, false, nil
	}
	params := map[string]any{"_meta": map[string]any{
		BridgePIDMetaKey: 103, BridgeStartMetaKey: "Sun Oct 4 06:00:00 2026", HostMetaKey: s.eng.HostID(),
	}}
	for range 3 {
		result(t, rpc(t, srv, "2026-07-28", "server/discover", params), "bridge discovery")
	}
	if n := probes.Load(); n != 2 {
		t.Fatalf("failed probe must retry and completed negative probe must cache; probes=%d, want2", n)
	}
}
