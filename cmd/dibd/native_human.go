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
	// human is the relay door, whose key store and sessions a device grant
	// or revocation changes.
	human *humanAPI
}

func registerNativeHumanAPI(mux *http.ServeMux, eng *engine.Engine, human *humanAPI) *nativeHumanAPI {
	h := &nativeHumanAPI{eng: eng, verify: notify.VerifyNativePeer, human: human}
	mux.HandleFunc("POST /api/human/native/message", h.message)
	mux.HandleFunc("POST /api/human/native/answer", h.answer)
	mux.HandleFunc("POST /api/human/native/media", h.media)
	mux.HandleFunc("POST /api/human/native/upload", h.upload)
	mux.HandleFunc("POST /api/human/native/devices", h.grantDevice)
	mux.HandleFunc("POST /api/human/native/devices/list", h.listDevices)
	mux.HandleFunc("POST /api/human/native/devices/revoke", h.revokeDevice)
	return h
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
	Node        string   `json:"node"`
	Serial      uint64   `json:"serial"`
	Action      string   `json:"action"`
	Text        string   `json:"text"`
	Choice      string   `json:"choice"`
	Submission  string   `json:"submission"`
	Attachments []string `json:"attachments"`
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
	// A queue that cannot be read costs the window its next and previous
	// steps, never the message the person clicked.
	queue, err := h.eng.NativeHumanQueue(r.Context(), req.Node, req.Serial)
	if err != nil {
		slog.Warn("native human queue unavailable", "error", err)
	}
	maxBytes, maxFiles := h.eng.HumanUploadLimits()
	nativeHumanJSON(w, map[string]any{
		"ok": true, "view": view, "title": title, "queue": queue,
		"upload_limits": map[string]any{"bytes": maxBytes, "files": maxFiles},
	})
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
	var filesError string
	if _, err := h.eng.HumanReplyFiles(r.Context(), req.Serial, req.Attachments, req.Submission); err != nil {
		filesError = "Your reply was recorded, but its files were not sent. Open the board to attach them again."
		slog.Warn("native human reply recorded without its files", "msg", req.Serial, "error", err)
	}
	// The answer is recorded whatever happens next: a queue that cannot be read
	// only means the window closes instead of opening the next message.
	queue, err := h.eng.NativeHumanQueue(r.Context(), req.Node, req.Serial)
	if err != nil {
		slog.Warn("native human queue unavailable", "error", err)
	}
	nativeHumanJSON(w, map[string]any{"ok": true, "next": queue.Next, "files_error": filesError})
}

func nativeHumanJSON(w http.ResponseWriter, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "cannot encode native notification", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
