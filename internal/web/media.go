// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package web

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/agenxy/dibs/internal/core"
)

func (s *Server) registerMedia(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/messages/{serial}/attachments/{index}", s.attachment)
	mux.HandleFunc("POST /api/upload", s.upload)
}

// The board guard supplies human authority before either route is reached.
// Bytes are decrypted outside the writer by the same engine path as get_blob.
func (s *Server) attachment(w http.ResponseWriter, r *http.Request) {
	serial, serialErr := strconv.ParseUint(r.PathValue("serial"), 10, 64)
	index, indexErr := strconv.Atoi(r.PathValue("index"))
	if serialErr != nil || indexErr != nil || index < 0 {
		mediaRefuse(w, http.StatusNotFound, "no such attachment", "read the retained message on the board")
		return
	}
	file, err := s.eng.MessageAttachment(r.Context(), serial, index, false)
	if err != nil {
		code, hint := http.StatusInternalServerError, "retry on the board; the attachment could not be read"
		switch {
		case errors.Is(err, core.ErrNoBlob):
			code, hint = http.StatusNotFound, "read the retained message or ask its sender for the file"
		case errors.Is(err, core.ErrBlobEvicted):
			code, hint = http.StatusGone, "the attachment expired; ask its sender to attach it again"
		}
		mediaRefuse(w, code, err.Error(), hint)
		return
	}
	contentType, disposition := "application/octet-stream", "attachment"
	if file.Kind == "image" || file.Kind == "video" {
		contentType, disposition = file.Mime, "inline"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Dibs-Media-Kind", file.Kind)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, fmt.Sprintf("attachment-%d-%d", serial, index), time.Time{}, bytes.NewReader(file.Data))
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	mime := r.Header.Get("Content-Type")
	if !core.ValidMime(mime) {
		mediaRefuse(w, http.StatusBadRequest, "invalid attachment type", "send the file's MIME type in Content-Type")
		return
	}
	limit, _ := s.eng.HumanUploadLimits()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(limit)+1))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || len(data) > limit {
		mediaRefuse(w, http.StatusRequestEntityTooLarge, "file exceeds the attachment limit", "choose a smaller file")
		return
	}
	if err != nil {
		mediaRefuse(w, http.StatusBadRequest, "file upload could not be read", "choose the file again and retry")
		return
	}
	res, err := s.eng.HumanPutBlob(r.Context(), data, mime)
	if err != nil {
		mediaRefuse(w, http.StatusBadRequest, err.Error(), "choose a smaller file or retry the upload")
		return
	}
	writeActJSON(w, http.StatusOK, res)
}

func mediaRefuse(w http.ResponseWriter, status int, message, hint string) {
	writeActJSON(w, status, map[string]any{"error": message, "hint": hint})
}
