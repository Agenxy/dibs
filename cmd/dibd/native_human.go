// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/humanask"
	"github.com/agenxy/dibs/internal/localtransport"
	"github.com/agenxy/dibs/internal/notify"
)

type nativeHumanAPI struct {
	eng    *engine.Engine
	verify func(context.Context, []byte) error
}

func registerNativeHumanAPI(mux *http.ServeMux, eng *engine.Engine) {
	h := &nativeHumanAPI{eng: eng, verify: notify.VerifyNativePeer}
	mux.HandleFunc("POST /api/human/native/message", h.message)
	mux.HandleFunc("POST /api/human/native/answer", h.answer)
}

func (h *nativeHumanAPI) attest(w http.ResponseWriter, r *http.Request) bool {
	audit := localtransport.PeerAuditToken(r.Context())
	if !localtransport.IsLocal(r.Context()) || len(audit) != 32 {
		humanRefuse(w, http.StatusUnauthorized, "no native peer audit token",
			"open this notification with the installed Dibs app or use the board")
		return false
	}
	if err := h.verify(r.Context(), audit); err != nil {
		slog.Warn("native human notification peer refused", "error", err)
		humanRefuse(w, http.StatusForbidden, "not the installed native notifier", err.Error())
		return false
	}
	return true
}

type nativeHumanRequest struct {
	Node       string `json:"node"`
	Serial     uint64 `json:"serial"`
	Action     string `json:"action"`
	Text       string `json:"text"`
	Choice     string `json:"choice"`
	Submission string `json:"submission"`
}

func (h *nativeHumanAPI) message(w http.ResponseWriter, r *http.Request) {
	if !h.attest(w, r) {
		return
	}
	var req nativeHumanRequest
	if !decodeBody(w, r, &req) {
		return
	}
	view, err := h.eng.NativeHumanMessage(r.Context(), req.Node, req.Serial)
	if err != nil {
		humanRefuse(w, http.StatusNotFound, err.Error(), "use the board that posted this notification")
		return
	}
	from := view.Notice.FromName
	if from == "" {
		from = view.Notice.From
	}
	title := "Dibs · " + humanask.OneLine(from) + " asks"
	if view.Notice.Type == "request" {
		adopt := view.Notice.AdoptName
		if adopt == "" {
			adopt = view.Notice.Adopt
		}
		title = humanask.RequestTitle(from, view.Notice.Grant, adopt, view.Notice.Merge)
	}
	nativeHumanJSON(w, http.StatusOK, map[string]any{"ok": true, "view": view, "title": title})
}

func (h *nativeHumanAPI) answer(w http.ResponseWriter, r *http.Request) {
	if !h.attest(w, r) {
		return
	}
	var req nativeHumanRequest
	if !decodeBodyLimit(w, r, &req, 1<<18) {
		return
	}
	view, err := h.eng.NativeHumanMessage(r.Context(), req.Node, req.Serial)
	if err != nil {
		humanRefuse(w, http.StatusNotFound, err.Error(), "use the original board")
		return
	}
	reply := notify.PromptReply{Action: req.Action, Choice: req.Choice, Text: req.Text}
	if err := notify.ValidateNativeReply(view.Notice.Type, view.Notice.Choices, reply); err != nil {
		humanRefuse(w, http.StatusBadRequest, err.Error(), "choose an offered option or write a reply")
		return
	}
	body := req.Text
	if req.Choice != "" {
		body = req.Choice
		if req.Text != "" {
			body += "\n\n" + req.Text
		}
	}
	if err := h.eng.AnswerNativeHuman(r.Context(), req.Node, req.Serial, req.Action, body, req.Submission); err != nil {
		humanRefuse(w, http.StatusConflict, err.Error(), "read the current message state; no second decision was recorded")
		return
	}
	slog.Info("the human answered from the native notification", "msg", req.Serial, "disposition", req.Action)
	nativeHumanJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func nativeHumanJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "cannot encode native notification", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
