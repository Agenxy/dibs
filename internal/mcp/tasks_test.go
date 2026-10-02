package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

// withTasks is a tools/call's params from a client that declares the tasks
// extension on this request, as the extension requires.
func withTasks(args map[string]any) map[string]any {
	return taskCapabilities(map[string]any{"name": "send", "arguments": args})
}

func taskCapabilities(params map[string]any) map[string]any {
	params["_meta"] = map[string]any{"io.modelcontextprotocol/clientCapabilities": map[string]any{
		"extensions": map[string]any{tasksExt: map[string]any{}},
	}}
	return params
}

func taskPair(t *testing.T) (srvURL func(string, any) map[string]any, lead, worker string) {
	t.Helper()
	srv, _ := newServer(t)
	l := toolCall(t, srv, "register", map[string]any{"name": "lead", "cwd": t.TempDir()})
	w := toolCall(t, srv, "register", map[string]any{"name": "worker", "cwd": t.TempDir()})
	lead, worker = l["token"].(string), w["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": lead})
	toolCall(t, srv, "check_in", map[string]any{"token": worker})
	return func(method string, params any) map[string]any {
		return rpc(t, srv, "2026-07-28", method, params)
	}, lead, worker
}

// A sender that asks to track a request, on a host that declared tasks,
// gets a task handle; the request's life is the task's: working while it
// waits and while milestones are reported, the newest step in its status
// message, and completed with the deliverable when done.
func TestATrackedRequestIsAnMCPTask(t *testing.T) {
	call, lead, worker := taskPair(t)
	disc := call("server/discover", map[string]any{})
	exts, _ := disc["result"].(map[string]any)["capabilities"].(map[string]any)["extensions"].(map[string]any)
	if _, ok := exts[tasksExt]; !ok {
		t.Fatal("the tasks extension is not advertised, so no host will offer it")
	}

	sent := call("tools/call", withTasks(map[string]any{
		"token": lead, "to": "worker", "type": "request", "body": "write the report",
		"milestones": `["sources gathered","draft written"]`, "track": "true",
	}))["result"].(map[string]any)
	if sent["resultType"] != "task" || sent["status"] != "working" {
		t.Fatalf("a tracked send to a tasks host returned %v, want a task handle", sent)
	}
	id, _ := sent["taskId"].(string)
	serial := uint64(sent["_meta"].(map[string]any)["com.dibs/msg_serial"].(float64))
	if !strings.Contains(sent["statusMessage"].(string), "not yet accepted") {
		t.Errorf("status %q", sent["statusMessage"])
	}

	get := func() map[string]any {
		t.Helper()
		out := call("tasks/get", taskCapabilities(map[string]any{"taskId": id}))
		res, ok := out["result"].(map[string]any)
		if !ok {
			t.Fatalf("tasks/get: %v", out)
		}
		return res
	}
	toolCall2 := func(args map[string]any) {
		t.Helper()
		out := call("tools/call", map[string]any{"name": "respond", "arguments": args})
		if r, _ := out["result"].(map[string]any); r == nil || r["isError"] == true {
			t.Fatalf("respond %v: %v", args, out)
		}
	}
	toolCall2(map[string]any{"token": worker, "msg_serial": serial, "disposition": "approve"})
	toolCall2(map[string]any{
		"token": worker, "msg_serial": serial, "disposition": "progress",
		"milestone": 1, "body": "twelve sources", "deliverable": "/work/sources.md",
	})
	got := get()
	msg, _ := got["statusMessage"].(string)
	for _, want := range []string{"1 of 2", "sources gathered", "twelve sources", "/work/sources.md"} {
		if got["status"] != "working" || !strings.Contains(msg, want) {
			t.Errorf("mid-task: %s %q, want working and %q", got["status"], msg, want)
		}
	}
	toolCall2(map[string]any{"token": worker, "msg_serial": serial, "disposition": "done", "deliverable": "/reports/q3.md"})
	got = get()
	if got["status"] != "completed" {
		t.Fatalf("after done the task is %v", got["status"])
	}
	result, _ := got["result"].(map[string]any)
	if sc, _ := result["structuredContent"].(map[string]any); sc["deliverable"] != "/reports/q3.md" || result["isError"] != false {
		t.Errorf("the completed result is %v", result)
	}
}

// Only on request: an untracked send, or a host that did not declare tasks
// on this request, gets the ordinary result, because a host that predates
// tasks may wait on one until it finishes and a request can take hours.
func TestATaskIsReturnedOnlyWhenAskedForAndDeclared(t *testing.T) {
	call, lead, _ := taskPair(t)
	for name, params := range map[string]map[string]any{
		"untracked": withTasks(map[string]any{"token": lead, "to": "worker", "type": "request", "body": "a"}),
		"undeclared": {"name": "send", "arguments": map[string]any{
			"token": lead, "to": "worker", "type": "request", "body": "b", "track": true,
		}},
	} {
		res := call("tools/call", params)["result"].(map[string]any)
		if res["resultType"] == "task" {
			t.Errorf("%s: a task was returned: %v", name, res)
		}
		if name == "undeclared" {
			var result map[string]any
			text := res["content"].([]any)[0].(map[string]any)["text"].(string)
			if err := json.Unmarshal([]byte(text), &result); err != nil {
				t.Fatal(err)
			}
			note, _ := result["tracking"].(string)
			if !strings.Contains(note, tasksExt) || !strings.Contains(note, "read_mail") {
				t.Errorf("tracking without a capable host was silent: %v", result)
			}
		}
	}
}

func TestTasksAreNotReturnedOrServedInTheLegacyEra(t *testing.T) {
	srv, _ := newServer(t)
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead"})["token"].(string)
	toolCall(t, srv, "register", map[string]any{"name": "worker"})
	toolCall(t, srv, "check_in", map[string]any{"token": lead})
	params := withTasks(map[string]any{
		"token": lead, "to": "worker", "type": "request", "body": "x", "track": true,
	})
	legacy := rpc(t, srv, "2025-11-25", "tools/call", params)["result"].(map[string]any)
	if legacy["resultType"] == "task" {
		t.Errorf("legacy tool result became a task: %v", legacy)
	}
	modern := rpc(t, srv, "2026-07-28", "tools/call", params)["result"].(map[string]any)
	for _, method := range []string{"tasks/get", "tasks/update", "tasks/cancel"} {
		out := rpc(t, srv, "2025-11-25", method, taskCapabilities(map[string]any{"taskId": modern["taskId"]}))
		err, _ := out["error"].(map[string]any)
		if err["code"] != float64(-32601) {
			t.Errorf("%s was served in the legacy era: %v", method, out)
		}
	}
}

// A task id is a bearer handle, so it cannot be guessed from a serial, and
// one this board did not issue answers nothing.
func TestATaskIDCannotBeForged(t *testing.T) {
	call, lead, _ := taskPair(t)
	sent := call("tools/call", withTasks(map[string]any{
		"token": lead, "to": "worker", "type": "request", "body": "x", "track": true,
	}))["result"].(map[string]any)
	id := sent["taskId"].(string)
	serial := strings.Split(id, "-")[1]
	for _, forged := range []string{"dibs-" + serial + "-00000000000000000000000000000000", "dibs-" + serial, "786512e2", ""} {
		if out := call("tasks/get", taskCapabilities(map[string]any{"taskId": forged})); out["error"] == nil {
			t.Errorf("tasks/get answered the forged id %q: %v", forged, out)
		}
	}
}

// A host following the task is pushed each change, carrying the whole task,
// and the stream ends when the task does.
func TestATaskSubscriptionPushesEachChange(t *testing.T) {
	srv, _ := newServer(t)
	l := toolCall(t, srv, "register", map[string]any{"name": "lead", "cwd": t.TempDir()})
	w := toolCall(t, srv, "register", map[string]any{"name": "worker", "cwd": t.TempDir()})
	lead, worker := l["token"].(string), w["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": lead})
	toolCall(t, srv, "check_in", map[string]any{"token": worker})
	sent := rpc(t, srv, "2026-07-28", "tools/call", withTasks(map[string]any{
		"token": lead, "to": "worker", "type": "request", "body": "x", "track": true,
	}))["result"].(map[string]any)
	id := sent["taskId"].(string)
	serial := uint64(sent["_meta"].(map[string]any)["com.dibs/msg_serial"].(float64))

	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "tasks", "method": "subscriptions/listen",
		"params": taskCapabilities(map[string]any{"notifications": map[string]any{"taskIds": []string{id, "dibs-1-forged"}}}),
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := srv.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lines := scanLines(ctx, resp)
	nextTask := func() map[string]any {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case line, open := <-lines:
				if !open {
					return nil
				}
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok {
					continue
				}
				var msg map[string]any
				_ = json.Unmarshal([]byte(data), &msg)
				switch msg["method"] {
				case "notifications/subscriptions/acknowledged":
					ids, _ := msg["params"].(map[string]any)["notifications"].(map[string]any)["taskIds"].([]any)
					if len(ids) != 1 || ids[0] != id {
						t.Errorf("acknowledged %v, want only the real task", ids)
					}
				case "notifications/tasks":
					return msg["params"].(map[string]any)
				}
			case <-deadline:
				t.Fatal("no task notification arrived")
			}
		}
	}
	if first := nextTask(); first["status"] != "working" || first["taskId"] != id {
		t.Fatalf("the first notification is %v, want the task's current state", first)
	}
	toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": serial, "disposition": "deny", "body": "not now"})
	last := nextTask()
	if last == nil || last["status"] != "completed" {
		t.Fatalf("after the denial the stream sent %v", last)
	}
	if r, _ := last["result"].(map[string]any); r["isError"] != true {
		t.Errorf("a denied request completed without isError: %v", r)
	}
	select {
	case _, open := <-lines:
		for open {
			_, open = <-lines
		}
	case <-time.After(5 * time.Second):
		t.Error("the stream stayed open after its only task finished")
	}
}

// A parameter an old host's schema does not know arrives as a string; the
// repair is unambiguous where the target is an array, a number or a flag,
// and leaves everything else alone (Dibs #7634).
func TestArgumentsSentAsStringsAreRepairedOnlyWhereUnambiguous(t *testing.T) {
	in := json.RawMessage(`{"milestones":"[\"a\",\"b\"]","milestone":"2","track":"true","body":"[not json","to":"5"}`)
	var a toolArgs
	if err := json.Unmarshal(unstring(in), &a); err != nil {
		t.Fatalf("the repaired arguments do not decode: %v", err)
	}
	if len(a.Milestones) != 2 || a.Milestone != 2 || !a.Track {
		t.Errorf("repaired to %+v", a)
	}
	if a.Body != "[not json" || a.To != "5" {
		t.Errorf("a string field was rewritten: body %q, to %q", a.Body, a.To)
	}
}

// Capabilities belong to each request, including task reads and listeners:
// declaring support when sending a request does not authorize later calls.
func TestTaskMethodsRequireCapabilitiesOnEveryRequest(t *testing.T) {
	call, lead, _ := taskPair(t)
	sent := call("tools/call", withTasks(map[string]any{
		"token": lead, "to": "worker", "type": "request", "body": "x", "track": true,
	}))["result"].(map[string]any)
	for _, method := range []string{"tasks/get", "tasks/update", "tasks/cancel", "subscriptions/listen"} {
		params := map[string]any{"taskId": sent["taskId"]}
		if method == "subscriptions/listen" {
			params = map[string]any{"notifications": map[string]any{
				"taskIds": []string{"not-a-task"}, "resourceSubscriptions": []string{"dibs://unknown"},
			}}
		}
		out := call(method, params)
		e, _ := out["error"].(map[string]any)
		if e["code"] != float64(-32021) {
			t.Errorf("%s without capabilities: %v, want -32021", method, out)
			continue
		}
		data, _ := e["data"].(map[string]any)
		required, _ := data["requiredCapabilities"].(map[string]any)
		exts, _ := required["extensions"].(map[string]any)
		if _, ok := exts[tasksExt]; !ok {
			t.Errorf("%s error does not identify the missing extension: %v", method, e)
		}
	}
}

// Repairing a number must emit JSON numbers: +02 parses as an integer but
// cannot be inserted into JSON verbatim, or it undoes every other repair.
func TestStringNumberRepairDoesNotUndoArrayRepair(t *testing.T) {
	var a toolArgs
	err := json.Unmarshal(unstring(json.RawMessage(`{"milestones":"[\"a\"]","milestone":"+02"}`)), &a)
	if err != nil || a.Milestone != 2 || len(a.Milestones) != 1 {
		t.Fatalf("number and array repair: %+v, %v", a, err)
	}
}

type pausedTaskWriter struct {
	*httptest.ResponseRecorder
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (w *pausedTaskWriter) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte(`"method":"notifications/tasks"`)) {
		w.once.Do(func() {
			close(w.entered)
			<-w.release
		})
	}
	return w.ResponseRecorder.Write(b)
}

// Drive the real listener with a slow writer, fill its live buffer with
// unrelated events, and drop its terminal update. The task snapshot must
// still arrive and end the stream, even with no later event for that task.
func TestTaskSubscriptionRecoversADroppedCompletion(t *testing.T) {
	srv, eng, s := newServerWithEngine(t)
	lead := toolCall(t, srv, "register", map[string]any{"name": "lead"})["token"].(string)
	worker := toolCall(t, srv, "register", map[string]any{"name": "worker"})["token"].(string)
	toolCall(t, srv, "check_in", map[string]any{"token": lead})
	toolCall(t, srv, "check_in", map[string]any{"token": worker})
	sent := rpc(t, srv, "2026-07-28", "tools/call", withTasks(map[string]any{
		"token": lead, "to": "worker", "type": "request", "body": "x", "track": true,
	}))["result"].(map[string]any)
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "subscriptions/listen",
		"params": taskCapabilities(map[string]any{"notifications": map[string]any{
			"taskIds": []string{sent["taskId"].(string)},
		}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	w := &pausedTaskWriter{
		ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{}),
	}
	resume := sync.OnceFunc(func() { close(w.release) })
	finished := make(chan struct{})
	go func() { s.ServeHTTP(w, req); close(finished) }()
	defer func() { resume(); cancel(); <-finished }()
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("listener never reached its first task write")
	}
	for i := range 300 {
		if err := eng.SetRateTokens(ctx, "lead", 1000); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.Do(ctx, &core.Op{Kind: core.OpUpdate, Token: lead, Description: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	serial := uint64(sent["_meta"].(map[string]any)["com.dibs/msg_serial"].(float64))
	toolCall(t, srv, "respond", map[string]any{"token": worker, "msg_serial": serial, "disposition": "deny"})
	resume()
	select {
	case <-finished:
		if !strings.Contains(w.Body.String(), `"status":"completed"`) {
			t.Fatalf("the dropped completion was not recovered: %s", w.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener lost completion and stayed open")
	}
}
