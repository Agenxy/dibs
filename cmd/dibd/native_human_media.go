// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
)

const nativeHumanImageLimit = 16 << 20

func (h *nativeHumanAPI) media(w http.ResponseWriter, r *http.Request) {
	if !h.attest(w, r) {
		return
	}
	var req struct {
		Node   string `json:"node"`
		Serial uint64 `json:"serial"`
		Index  int    `json:"index"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	view, err := h.eng.NativeHumanMessage(r.Context(), req.Node, req.Serial)
	if err != nil || req.Index < 0 || req.Index >= len(view.Media) {
		humanRefuse(w, http.StatusNotFound, "no image on this human message", "open the original board")
		return
	}
	media := view.Media[req.Index]
	if media.Expired {
		humanRefuse(w, http.StatusGone, "attachment expired", "open the board to inspect the retained message")
		return
	}
	if media.Kind != "image" || media.File {
		humanRefuse(w, http.StatusUnsupportedMediaType, "this attachment is not a raster image", "open the file on the board")
		return
	}
	if media.Size > nativeHumanImageLimit {
		humanRefuse(w, http.StatusRequestEntityTooLarge,
			"image exceeds the native 16 MiB preview limit", "open the image on the board")
		return
	}
	file, err := h.eng.MessageAttachment(r.Context(), req.Serial, req.Index, true)
	if err != nil {
		humanRefuse(w, http.StatusNotFound, err.Error(), "open the original board")
		return
	}
	if file.Kind != "image" || len(file.Data) > nativeHumanImageLimit {
		humanRefuse(w, http.StatusRequestEntityTooLarge,
			"image cannot be previewed within the native limit", "open the image on the board")
		return
	}
	nativeHumanJSON(w, map[string]any{"ok": true, "mime": file.Mime, "kind": file.Kind, "data": file.Data})
}

func (h *nativeHumanAPI) upload(w http.ResponseWriter, r *http.Request) {
	if !h.attest(w, r) {
		return
	}
	maxBytes, _ := h.eng.HumanUploadLimits()
	var req struct {
		Node string `json:"node"`
		Name string `json:"name"`
		Mime string `json:"mime"`
		Data string `json:"data"`
	}
	// Encoded bytes plus a bounded envelope; answer keeps its 256 KiB cap.
	limit := int64(base64.StdEncoding.EncodedLen(maxBytes)) + (16 << 10)
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(&req)
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		humanRefuse(w, status, "file upload did not parse within its limit", "choose a smaller file or open the board")
		return
	}
	if req.Node != h.eng.NodeID() {
		humanRefuse(w, http.StatusConflict, "upload belongs to another board", "reopen the original notification")
		return
	}
	if req.Name == "" || len(req.Name) > 1024 || len(req.Mime) > 512 {
		humanRefuse(w, http.StatusBadRequest, "file name or type is invalid", "choose a file with a shorter name")
		return
	}
	if len(req.Data) > base64.StdEncoding.EncodedLen(maxBytes) {
		humanRefuse(w, http.StatusRequestEntityTooLarge,
			"file exceeds the attachment limit", "choose a smaller file or open the board")
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		humanRefuse(w, http.StatusBadRequest, "file data is not base64", "encode the file bytes as base64")
		return
	}
	if len(data) > maxBytes {
		humanRefuse(w, http.StatusRequestEntityTooLarge,
			"file exceeds the attachment limit", "choose a smaller file or open the board")
		return
	}
	result, err := h.eng.HumanPutBlob(r.Context(), data, req.Mime)
	if err != nil {
		humanRefuse(w, http.StatusBadRequest, err.Error(), "choose a smaller file or open the board")
		return
	}
	nativeHumanJSON(w, map[string]any{"ok": true, "blob": result["blob"], "size": result["size"]})
}
