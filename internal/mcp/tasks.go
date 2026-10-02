package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
)

// A request its sender follows, as an MCP task (the io.modelcontextprotocol/tasks
// extension, 2026-07-28; ~/Desktop/harnesses/ext-tasks).
//
// Dibs already had the thing: a request goes pending, approved, reports
// progress against milestones, and ends done with a deliverable, or denied,
// declined or expired. The extension is the standard way for a HOST to follow
// such a thing, so a sender that asks for it (`send(track: true)`) and whose
// host declares the extension gets a task handle instead of the ordinary
// result, and its host follows the request with tasks/get or a
// notifications/tasks subscription.
//
// Opt-in per send, on purpose. The extension allows a host that predates it
// to drive the polling internally and surface only the final result, and for
// a request that is a model waiting, possibly for hours, for another agent to
// finish work. Coordination here is asynchronous and must stay so unless the
// sender chose otherwise (AGENTS.md rule 4).
//
// The spec forbids notifications/progress on tasks; progress travels in the
// task's statusMessage, which is what this does with milestones.

// tasksExt is the extension's identifier.
const tasksExt = "io.modelcontextprotocol/tasks"

// taskPollMs is the polling interval a task suggests. Work worth tracking
// moves on the scale of minutes; a host polling faster learns nothing more.
const taskPollMs = 30_000

// Set once from this request's transport header or stateless metadata,
// never from a previous handshake or the client's claimed capabilities.
type requestEraKey struct{}

func modernTaskRequest(ctx context.Context) bool {
	era, _ := ctx.Value(requestEraKey{}).(string)
	return isModern(era)
}

// clientDeclaresTasks reports whether THIS request's capabilities include
// the extension. Per request, never remembered: the spec says a task goes
// only to a client that declared it on the request that would receive it.
func clientDeclaresTasks(params json.RawMessage) bool {
	var p struct {
		Meta struct {
			Caps struct {
				Extensions map[string]any `json:"extensions"`
			} `json:"io.modelcontextprotocol/clientCapabilities"`
		} `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	_, ok := p.Meta.Caps.Extensions[tasksExt]
	return ok
}

// A previous declaration never substitutes for this request's capability.
func requireTasks(ctx context.Context, params json.RawMessage) *rpcError {
	if !modernTaskRequest(ctx) {
		return &rpcError{
			Code: -32601, Message: "tasks require MCP 2026-07-28",
			Data: hint("use protocolVersion 2026-07-28 and declare io.modelcontextprotocol/tasks on this request"),
		}
	}
	if clientDeclaresTasks(params) {
		return nil
	}
	return &rpcError{
		Code: -32021, Message: "Missing required client capability",
		Data: map[string]any{
			"requiredCapabilities": map[string]any{"extensions": map[string]any{tasksExt: map[string]any{}}},
			"hint":                 "declare io.modelcontextprotocol/tasks in this request's _meta clientCapabilities",
		},
	}
}

// SetTaskKey sets the secret task ids are derived from. dibd passes the
// board's own secret, so an id survives a restart with nothing stored, and a
// reset board (a new secret) invalidates every id along with everything else.
func (s *Server) SetTaskKey(secret string) {
	if secret != "" {
		s.taskKey = []byte(secret)
	}
}

func randomTaskKey() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

// taskID names a tracked request: its serial, and a MAC over it so the id
// cannot be guessed or forged from a serial, which the spec requires because
// ids are bearer handles here.
func (s *Server) taskID(serial uint64) string {
	m := hmac.New(sha256.New, s.taskKey)
	_, _ = fmt.Fprintf(m, "dibs-task/v1\n%s\n%d", s.eng.NodeID(), serial)
	return "dibs-" + strconv.FormatUint(serial, 10) + "-" + hex.EncodeToString(m.Sum(nil)[:16])
}

// taskSerial reverses taskID, refusing an id this board did not issue.
func (s *Server) taskSerial(id string) (uint64, bool) {
	rest, ok := strings.CutPrefix(id, "dibs-")
	if !ok {
		return 0, false
	}
	num, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	serial, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		return 0, false
	}
	return serial, hmac.Equal([]byte(id), []byte(s.taskID(serial)))
}

// detailedTask is the task as tasks/get and notifications/tasks state it.
func (s *Server) detailedTask(m core.Message) map[string]any {
	updated := m.SentAt
	if m.DeliveredTime.After(updated) {
		updated = m.DeliveredTime
	}
	if m.TerminalAt.After(updated) {
		updated = m.TerminalAt
	}
	for _, p := range m.Progress {
		if p.At.After(updated) {
			updated = p.At
		}
	}
	t := map[string]any{
		"taskId":         s.taskID(m.Serial),
		"createdAt":      m.SentAt.UTC().Format(time.RFC3339),
		"lastUpdatedAt":  updated.UTC().Format(time.RFC3339),
		"ttlMs":          core.TaskTTL.Milliseconds(),
		"pollIntervalMs": taskPollMs,
	}
	status, message, result := taskState(m)
	t["status"], t["statusMessage"] = status, message
	if result != nil {
		t["result"] = result
	}
	return t
}

// taskState maps the request's lifecycle onto the task's. Denied, declined
// and expired are results, not protocol errors, so they complete with
// isError: "failed" is reserved for a JSON-RPC error, which none of these is.
func taskState(m core.Message) (status, message string, result map[string]any) {
	switch m.State {
	case core.MsgStatePending, core.MsgStateDelivered, "":
		return "working", "sent to " + m.To + "; not yet accepted", nil
	case core.MsgStateApproved:
		return "working", progressLine(m), nil
	case core.MsgStateDone:
		text := m.To + " reports it done"
		if m.Response != "" {
			text += ": " + m.Response
		}
		if m.Deliverable != "" {
			text += ". Delivered at " + m.Deliverable
		}
		return "completed", text, toolResult(m, text, false)
	default:
		text := m.To + " " + m.State
		if m.Response != "" {
			text += ": " + m.Response
		}
		return "completed", text, toolResult(m, text, true)
	}
}

// progressLine is the statusMessage of an accepted request: how far along,
// the newest step, and the newest note.
func progressLine(m core.Message) string {
	line := m.To + " accepted it"
	if n := len(m.Milestones); n > 0 {
		line = fmt.Sprintf("%d of %d milestones", m.Reached(), n)
	}
	for i := len(m.Progress) - 1; i >= 0; i-- {
		p := m.Progress[i]
		if p.Review != "" {
			continue
		}
		if p.Milestone > 0 && p.Milestone <= len(m.Milestones) {
			line += "; reached " + strconv.Quote(m.Milestones[p.Milestone-1])
		}
		if p.Note != "" {
			line += ": " + p.Note
		}
		if p.Artifact != "" {
			line += " (check " + p.Artifact + ")"
		}
		break
	}
	return line
}

// toolResult is what the send would have returned had it waited: a
// CallToolResult with the outcome in text and in structure.
func toolResult(m core.Message, text string, isErr bool) map[string]any {
	sc := map[string]any{"msg_serial": m.Serial, "state": m.State}
	if m.Deliverable != "" {
		sc["deliverable"] = m.Deliverable
	}
	if len(m.Milestones) > 0 {
		sc["milestones"], sc["reached"] = m.Milestones, m.Reached()
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": text}},
		"structuredContent": sc,
		"isError":           isErr,
	}
}

// createTaskResult is a send's answer when it became a task. The ordinary
// result's msg_serial rides in _meta, so a host that shows the model the
// task can still hand it the serial read_mail takes.
func (s *Server) createTaskResult(ctx context.Context, res core.Result) (map[string]any, bool) {
	serial, _ := res["msg_serial"].(uint64)
	if serial == 0 {
		return nil, false
	}
	m, found, err := s.eng.TrackedMessage(ctx, serial)
	if err != nil || !found {
		return nil, false
	}
	t := s.detailedTask(m)
	delete(t, "result")
	t["resultType"] = "task"
	t["_meta"] = map[string]any{"com.dibs/msg_serial": serial}
	return t, true
}

// Tracking without a capable modern host still keeps the request, but it
// must say that no host is following a task handle. Otherwise the ordinary
// successful send result silently looks like successful host tracking.
func (s *Server) trackingResult(ctx context.Context, params json.RawMessage, res core.Result) (map[string]any, bool) {
	reason := "the task handle is unavailable"
	switch {
	case !modernTaskRequest(ctx):
		reason = "tasks require MCP 2026-07-28; this call uses the legacy protocol"
	case !clientDeclaresTasks(params):
		reason = "this call's host did not declare " + tasksExt
	default:
		if t, ok := s.createTaskResult(ctx, res); ok {
			return t, true
		}
	}
	res["tracking"] = "no task handle: " + reason + ". Follow the request with read_mail(msg_serial); " +
		"it is retained for seven days unless its recipient is purged first"
	return nil, false
}

// taskParams reads the taskId every tasks/ method carries.
func taskParams(params json.RawMessage) string {
	var p struct {
		TaskID string `json:"taskId"`
	}
	_ = json.Unmarshal(params, &p)
	return p.TaskID
}

// errNoTask is a task id this board did not issue, or whose request is gone.
func errNoTask() *rpcError {
	return &rpcError{
		Code: -32602, Message: "no such task",
		Data: hint("a task id comes from send(track: true) on this board, and lives " +
			"for its ttlMs unless its recipient is purged first or the board is reset; " +
			"read_mail on the msg_serial in its _meta still answers if the message is kept"),
	}
}

// getTask serves tasks/get.
func (s *Server) getTask(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	if err := requireTasks(ctx, params); err != nil {
		return nil, err
	}
	serial, ok := s.taskSerial(taskParams(params))
	if !ok {
		return nil, errNoTask()
	}
	m, found, err := s.eng.TrackedMessage(ctx, serial)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	if !found {
		return nil, errNoTask()
	}
	if i, invited := engine.InvitationFrom(ctx); invited {
		// Task handles are bearer capabilities on the private listener. An
		// invitation narrows that authority to its own identity as well.
		if i.Token == "" || (m.From != i.AgentID && m.To != i.AgentID) {
			return nil, &rpcError{
				Code: -32602, Message: "task does not belong to this invited agent",
				Data: hint("use your own task handle and _meta['" + metaTokenKey + "'] agent token"),
			}
		}
	}
	t := s.detailedTask(m)
	t["resultType"] = "complete"
	return t, nil
}

// ackTask serves tasks/update and tasks/cancel.
//
// update: a Dibs task never asks its client for input, so there is nothing a
// response could satisfy; the spec says to acknowledge and ignore.
// cancel: cooperative by the spec, and the work belongs to another agent
// that Dibs cannot stop (rule 4). It is acknowledged and the task carries on;
// a sender that wants the work stopped says so to the worker.
func (s *Server) ackTask(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	if _, err := s.getTask(ctx, params); err != nil {
		return nil, err
	}
	return map[string]any{"resultType": "complete"}, nil
}
