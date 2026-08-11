package web

import (
	"encoding/json"
	"net/http"

	"github.com/agenxy/lanes/internal/core"
)

// The human's write surface.
//
// Every handler here does the same thing: fetch the operator's own lane token,
// build the SAME op an agent would, and hand it to the engine. There is no
// admin shortcut, no direct state mutation, and nothing in internal/core that
// knows a human is involved.
//
// That is the design constraint, not an implementation detail. A parallel set of
// privileged write paths would be a second authorization surface into the state
// machine: unledgered unless each one remembered to ledger, invisible to
// `lanes verify`, and impossible for an agent to reason about. Routing the human
// through an ordinary lane means their post is an ordinary post, their question
// carries a real deadline, and the agent answering cannot tell it is talking to
// a person. Which is correct: it should behave the same either way.
//
// AUTH is inherited. These routes sit behind the same session cookie as the
// board itself, mintable only by proving the admin password
// (cmd/lanesd/guard.go). Reaching this code means that already happened.

// act is the one shape every write endpoint accepts, so the browser has one
// helper rather than seven.
type act struct {
	Lane        string `json:"lane"`        // channel id, for lane_* actions
	To          string `json:"to"`          // agent id, for send
	Body        string `json:"body"`        // message or post text
	Type        string `json:"type"`        // notify | question | request | handoff
	Serial      uint64 `json:"serial"`      // message being answered/acked
	Disposition string `json:"disposition"` // answer | approve | deny | decline
	DeadlineS   int    `json:"deadline_s"`
}

// registerActions mounts the human's write routes.
func (s *Server) registerActions(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me", s.apiMe)
	mux.HandleFunc("POST /api/act/{what}", s.apiAct)
}

// apiMe tells the page which AGENT it is speaking as, or that it is only
// watching, so the UI can mark the human's own memberships without guessing.
//
// The human is a participant, not a channel: they join the lanes agents open,
// and never need one of their own, and they need not be a participant at all.
func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	// Deliberately does NOT create. Watching the board is not participating:
	// an operator who has joined nothing owes nobody an acknowledgement and
	// should not appear on the roster. The identity is minted by the first
	// action, in apiAct.
	agent := s.eng.HumanIdentity()
	s.eng.HumanTouch(r.Context())
	// "agent", not "lane": this is new surface and free to use the clear word.
	// The board payload still says `lanes` for participants because that name is
	// frozen on disk (internal/ledger/wireformat_test.go).
	writeActJSON(w, http.StatusOK, map[string]any{"agent": agent})
}

// apiAct performs one action as the human.
func (s *Server) apiAct(w http.ResponseWriter, r *http.Request) {
	var a act
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&a); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	_, token, err := s.eng.HumanAgent(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	op, ok := a.op(r.PathValue("what"), token)
	if !ok {
		http.Error(w, "unknown action", http.StatusNotFound)
		return
	}

	res, err := s.eng.Do(r.Context(), op)
	if err != nil {
		// The engine's errors are written for an agent to act on: they name the
		// remedy, not just the fault, so they are worth showing verbatim rather
		// than flattening into "something went wrong".
		writeActJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeActJSON(w, http.StatusOK, res)
}

// op turns one action name into the op an agent would have sent.
//
// A plain dispatch table rather than a chain of handlers: every one of these is
// the same two lines, and the value of having them side by side is that you can
// see at a glance there is no privileged path hiding among them.
func (a act) op(what, token string) (*core.Op, bool) {
	op := &core.Op{Token: token}
	switch what {
	case "join":
		op.Kind, op.Channel = core.OpLaneJoin, a.Lane
	case "leave":
		op.Kind, op.Channel = core.OpLaneLeave, a.Lane
	case "post":
		op.Kind, op.Channel, op.Body = core.OpLanePost, a.Lane, a.Body
	case "announce":
		op.Kind, op.Channel, op.Body = core.OpLaneAnnounce, a.Lane, a.Body
	case "open":
		op.Kind, op.Channel, op.Text = core.OpLaneOpen, a.Lane, a.Body
	case "send":
		op.Kind, op.To, op.Body = core.OpSendMessage, a.To, a.Body
		op.MsgType, op.DeadlineSec = a.msgType(), a.deadline()
	case "respond":
		op.Kind, op.MsgSerial, op.Body = core.OpRespond, a.Serial, a.Body
		op.Disposition = a.disposition()
	case "ack":
		op.Kind, op.MsgSerial = core.OpAckMessage, a.Serial
	case "lane_ack":
		op.Kind, op.MsgSerial = core.OpLaneAck, a.Serial
	default:
		return nil, false
	}
	return op, true
}

func (a act) msgType() string {
	if a.Type == "" {
		return core.MsgNotify
	}
	return a.Type
}

// A human's question deserves a longer default deadline than an agent's: people
// step away from the keyboard, and a question that expires while its author is
// at lunch teaches the fleet that human questions do not matter.
func (a act) deadline() int {
	if a.DeadlineS <= 0 {
		return 3600
	}
	return a.DeadlineS
}

func (a act) disposition() string {
	if a.Disposition == "" {
		return "answer"
	}
	return a.Disposition
}

// writeActJSON is separate from util.go's writeJSON, which carries an error
// argument and a different status convention.
func writeActJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
