package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A refusal from the daemon's gate is a status and a line of text, and the
// bridge wrote that line to stdout as if it were JSON-RPC: the harness got
// `unauthorized`, no reply carrying its request id, and a call that never
// returned. The refusal is delivered as the error it is.
func TestARefusalFromTheGateBecomesAJSONRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	var buf bytes.Buffer
	out := &syncWriter{w: bufio.NewWriter(&buf)}
	line := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"check_in"}}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(line))
	if err != nil {
		t.Fatal(err)
	}
	forward(srv.Client(), req, line, out, nil)
	got := strings.TrimSpace(buf.String())
	var reply struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(got), &reply); err != nil {
		t.Fatalf("the harness read %q, which is not JSON-RPC: %v", got, err)
	}
	if string(reply.ID) != "7" {
		t.Fatalf("the reply carries id %s, want 7: the harness cannot match it to its call", reply.ID)
	}
	if !strings.Contains(reply.Error.Message, "401") || !strings.Contains(reply.Error.Message, "unauthorized") {
		t.Fatalf("the error says %q, and not what the daemon refused with", reply.Error.Message)
	}

	// A notification has no reply, refused or not.
	buf.Reset()
	note := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	req, err = http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(note))
	if err != nil {
		t.Fatal(err)
	}
	forward(srv.Client(), req, note, out, nil)
	if buf.Len() != 0 {
		t.Fatalf("a refused notification produced a line: %q", buf.String())
	}
}
