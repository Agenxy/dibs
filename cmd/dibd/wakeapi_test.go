package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/mcp"
)

// The report and the host list take the board's secret and nothing less; a
// report for a request the hub is not waiting on is refused, so a bridge that
// lost track learns it rather than believing it was heard; and a bridge whose
// stream is open is what /api/hosts lists, for doctor on the hub.
func TestTheWakeAPITakesTheSecretAndListsAttachedHosts(t *testing.T) {
	eng, _ := testEngine(t)
	const secret = "s3cret"
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.New(eng))
	registerWakeAPI(mux, eng, secret)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	post := func(auth, body string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/wake-result", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, _ := post("", `{"id":1,"host":"laptop","ok":true}`); code != http.StatusUnauthorized {
		t.Errorf("a report with no secret was answered %d", code)
	}
	if code, _ := post("Bearer wrong", `{"id":1,"host":"laptop","ok":true}`); code != http.StatusUnauthorized {
		t.Errorf("a report with the wrong secret was answered %d", code)
	}
	if code, _ := post("Bearer "+secret, `{"host":"laptop","ok":true}`); code != http.StatusBadRequest {
		t.Errorf("a report naming no request id was answered %d", code)
	}
	if code, out := post("Bearer "+secret, `{"id":7,"host":"laptop","ok":true}`); code != http.StatusNotFound || out["accepted"] == true {
		t.Errorf("a report for a request nobody is waiting on was answered %d %v", code, out)
	}

	hosts := func() []any {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/hosts", nil)
		req.Header.Set("X-Dibs-Local", secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		list, _ := out["hosts"].([]any)
		return list
	}
	if got := hosts(); len(got) != 0 {
		t.Fatalf("hosts before any bridge attached = %v", got)
	}
	if resp, err := http.Get(srv.URL + "/api/hosts"); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("the host list was served without the secret: %d", resp.StatusCode)
		}
	}

	// A bridge attaches by opening its wake stream; while it is open the hub
	// lists it, and when it closes the hub does not.
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "w", "method": "subscriptions/listen", "params": map[string]any{
		"notifications": map[string]any{"resourceSubscriptions": []string{mcp.WakeURI}},
		"_meta":         map[string]any{mcp.HostMetaKey: "laptop", mcp.WakeHarnessesMetaKey: []string{"codex"}},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("the wake stream did not open: %s", resp.Status)
	}
	buf := make([]byte, 1)
	_, _ = resp.Body.Read(buf) // the acknowledgement is on its way: the bridge is attached
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := hosts()
		if len(got) == 1 {
			row, _ := got[0].(map[string]any)
			if row["host"] != "laptop" {
				t.Errorf("hosts = %v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the attached bridge is not listed: %v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	_ = resp.Body.Close()
	deadline = time.Now().Add(5 * time.Second)
	for len(hosts()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("a bridge whose stream closed is still listed as attached")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
