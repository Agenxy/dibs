package transfer

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agenxy/dibs/internal/core"
	xport "github.com/agenxy/dibs/internal/transport"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	var domain *core.Error
	if !errors.As(err, &domain) {
		domain = refusal("E_TRANSFER_IO", "transfer I/O failed",
			"HEAD for the upload offset and retry; if failure persists authorize a fresh descriptor")
	}
	writeJSON(w, HTTPStatus(err), domain)
}

// Handler bypasses board authentication ONLY for exact random ticket resources.
// trustedProxy is set solely by the operator's loopback-only public-url config.
// That proxy owns TLS negotiation; forwarded client headers are never evidence.
func (m *Manager) Handler(origin string, trustedProxy bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		t, key, err := m.lookup(r, origin, trustedProxy)
		if err != nil {
			writeError(w, err)
			return
		}
		if !t.mu.TryLock() {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusConflict, refusal("E_TRANSFER_BUSY", "transfer has an active request",
				"retry HEAD after the other request finishes"))
			return
		}
		defer func() {
			m.mu.Lock()
			stopping := m.stopping
			m.mu.Unlock()
			if stopping && t.upload != nil {
				t.upload.Abort()
				t.upload = nil
			}
			t.mu.Unlock()
		}()
		m.serveTicket(w, r, t, key)
	})
}

func (m *Manager) lookup(r *http.Request, origin string, proxy bool) (*ticket, string, error) {
	key, upload := strings.CutPrefix(strings.TrimPrefix(r.URL.Path, Prefix), "up/")
	if !strings.HasPrefix(r.URL.Path, Prefix) || r.URL.RawQuery != "" || len(key) != 64 {
		return nil, "", gone()
	}
	m.mu.Lock()
	t := m.tickets[key]
	m.mu.Unlock()
	if t == nil || (origin != "" && t.origin != origin) || (t.blob == "") != upload {
		return nil, "", gone()
	}
	if err := transferTransport(r, t.origin, proxy); err != nil {
		return nil, "", err
	}
	return t, key, nil
}

func (m *Manager) serveTicket(w http.ResponseWriter, r *http.Request, t *ticket, key string) {
	ctx, err := m.authorizeRequest(r.Context(), t)
	if err != nil {
		m.release(t)
		t.failed = true
		writeError(w, err)
		return
	}
	if r.Method == http.MethodDelete {
		m.release(t)
		m.mu.Lock()
		delete(m.tickets, key)
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if t.blob == "" {
		m.serveUpload(ctx, w, r, t)
		return
	}
	m.serveDownload(w, r, t)
}

func (m *Manager) serveDownload(w http.ResponseWriter, r *http.Request, t *ticket) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, refusal("E_TRANSFER_METHOD", "download cannot receive bytes",
			"use GET, HEAD or DELETE on this descriptor"))
		return
	}
	reader, err := m.store.Open(t.blob)
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = reader.Close() }()
	w.Header().Set("ETag", "\""+t.blob+"\"")
	w.Header().Set("Content-Type", t.id.Mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Time{}, reader)
}

func transferTransport(r *http.Request, origin string, proxy bool) error {
	if given := r.Header.Get("Origin"); given != "" && given != origin {
		return refusal("E_ORIGIN", "forbidden transfer origin", "use the descriptor's configured origin")
	}
	loopback := xport.IsLoopback(r.RemoteAddr)
	if r.TLS != nil && r.TLS.Version >= tls.VersionTLS13 {
		return nil
	}
	if r.TLS == nil && loopback && (proxy || strings.HasPrefix(origin, "http://")) {
		return nil
	}
	return refusal("E_TRANSFER_TRANSPORT", "off-host transfers require TLS 1.3",
		"use TLS 1.3 with the MCP certificate; proxy operators must enforce TLS 1.3 at their public edge")
}

func uploadOffset(t *ticket) int64 {
	if t.upload != nil {
		return t.upload.Size()
	}
	if t.result != nil {
		size, _ := t.result["size"].(int64)
		return size
	}
	return 0
}

func uploadHeaders(w http.ResponseWriter, t *ticket) {
	w.Header().Set("Upload-Offset", strconv.FormatInt(uploadOffset(t), 10))
	complete := "?0"
	if t.result != nil {
		complete = "?1"
	}
	w.Header().Set("Upload-Complete", complete)
	w.Header().Set("Upload-Draft-Interop-Version", "9")
	w.Header().Set("Upload-Limit", "max-size="+strconv.FormatInt(t.id.MaxSize, 10))
	if t.size != nil {
		w.Header().Set("Upload-Length", strconv.FormatInt(*t.size, 10))
	}
}

func (m *Manager) prepareUpload(w http.ResponseWriter, r *http.Request, t *ticket) (final, ready bool) {
	uploadHeaders(w, t)
	if version := r.Header.Get("Upload-Draft-Interop-Version"); version != "" && version != "9" {
		writeJSON(w, http.StatusBadRequest, refusal("E_UPLOAD_VERSION", "unsupported resumable upload version",
			"use Upload-Draft-Interop-Version: 9"))
		return false, false
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusNoContent)
		return false, false
	}
	offset, final, err := parseUploadRequest(r)
	if err != nil {
		writeError(w, err)
		return false, false
	}
	if offset != uploadOffset(t) {
		writeJSON(w, http.StatusConflict, refusal("E_UPLOAD_OFFSET", "upload offset differs",
			"use this response's Upload-Offset, or HEAD, then PATCH the remaining source bytes"))
		return false, false
	}
	if t.result != nil {
		if final && r.ContentLength == 0 {
			writeJSON(w, http.StatusOK, t.result)
		} else {
			writeJSON(w, http.StatusConflict, refusal("E_UPLOAD_COMPLETE", "ticket already committed its file",
				"use the completed result; authorize another upload for other bytes"))
		}
		return false, false
	}
	if err := declareLength(r, t); err != nil {
		writeError(w, err)
		return false, false
	}
	if r.ContentLength > t.id.MaxSize-uploadOffset(t) {
		m.release(t)
		t.failed = true
		writeJSON(w, http.StatusRequestEntityTooLarge, core.ErrTooLargeBlob(int(t.id.MaxSize)))
		return false, false
	}
	return final, true
}

func parseUploadRequest(r *http.Request) (int64, bool, error) {
	if r.Method == http.MethodPut {
		return 0, true, nil
	}
	if r.Method != http.MethodPatch {
		return 0, false, refusal("E_TRANSFER_METHOD", "upload method unsupported",
			"use PUT once, or HEAD followed by PATCH to resume")
	}
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	complete := r.Header.Get("Upload-Complete")
	if err != nil || offset < 0 || (complete != "?0" && complete != "?1") ||
		r.Header.Get("Content-Type") != "application/partial-upload" {
		return 0, false, refusal("E_UPLOAD_HEADERS", "invalid resumable upload headers",
			"PATCH with application/partial-upload, Upload-Offset, and Upload-Complete: ?0 or ?1")
	}
	return offset, complete == "?1", nil
}

func declareLength(r *http.Request, t *ticket) error {
	if length := r.Header.Get("Upload-Length"); length != "" {
		n, err := strconv.ParseInt(length, 10, 64)
		if err != nil || n < 0 || n > t.id.MaxSize || (t.size != nil && n != *t.size) {
			return refusal("E_UPLOAD_LENGTH", "upload length differs from admission",
				"keep the authorized size, or authorize a new upload")
		}
		if t.size == nil {
			t.size = &n
		}
	}
	return nil
}

func (m *Manager) serveUpload(ctx context.Context, w http.ResponseWriter, r *http.Request, t *ticket) {
	final, ready := m.prepareUpload(w, r, t)
	if !ready {
		return
	}
	if err := m.copyUpload(w, r, t); err != nil {
		m.copyFailure(w, t, err)
		return
	}
	uploadHeaders(w, t)
	if !final {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if t.size != nil && uploadOffset(t) != *t.size {
		writeJSON(w, http.StatusConflict, refusal("E_UPLOAD_LENGTH", "upload has not reached its declared length",
			"PATCH remaining bytes at Upload-Offset with Upload-Complete: ?1"))
		return
	}
	if err := m.commitUpload(ctx, t); err != nil {
		m.release(t)
		t.failed = true
		writeError(w, err)
		return
	}
	uploadHeaders(w, t)
	writeJSON(w, http.StatusOK, t.result)
}

func (m *Manager) commitUpload(ctx context.Context, t *ticket) error {
	// Repeat revocation before commit; the writer repeats ID and issuer fencing.
	ctx, err := m.authorizeRequest(ctx, t)
	if err != nil {
		return err
	}
	blob, size, err := t.upload.Commit(t.hash)
	if err != nil {
		return err
	}
	defer m.store.Release(blob)
	t.result, err = m.eng.CommitTransfer(ctx, t.id, blob, size, t.mime)
	if err != nil {
		return err
	}
	t.upload = nil
	mime, _ := t.result["mime"].(string)
	t.result["file"] = fileValue(blob, t.name, mime, &size, strings.TrimPrefix(blob, "sha256:"))
	return nil
}

type uploadWriteError struct{ error }

func (e uploadWriteError) Unwrap() error { return e.error }

func (m *Manager) copyFailure(w http.ResponseWriter, t *ticket, err error) {
	uploadHeaders(w, t)
	var writeFailure uploadWriteError
	switch {
	case errors.Is(err, core.ErrBlobTooLarge):
		m.release(t)
		t.failed = true
		writeJSON(w, http.StatusRequestEntityTooLarge, core.ErrTooLargeBlob(int(t.id.MaxSize)))
	case errors.As(err, &writeFailure):
		m.release(t)
		t.failed = true
		writeError(w, err)
	default:
		// Read interruption retains accepted encrypted chunks and the exact offset.
		writeJSON(w, http.StatusServiceUnavailable, refusal("E_UPLOAD_INTERRUPTED", "upload request interrupted",
			"HEAD for the accepted offset, then PATCH the remaining bytes"))
	}
}

func (m *Manager) copyUpload(w http.ResponseWriter, r *http.Request, t *ticket) error {
	controller := http.NewResponseController(w)
	defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	buffer := make([]byte, 32*1024)
	emptyReads := 0
	for {
		if !m.now().Before(t.deadline) {
			return uploadWriteError{gone()}
		}
		// A stalled request cannot hold its ticket lock forever.
		_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := r.Body.Read(buffer)
		if n > 0 {
			if _, werr := t.upload.Write(buffer[:n]); werr != nil {
				return uploadWriteError{werr}
			}
			m.touch(t)
			emptyReads = 0
		} else {
			emptyReads++
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if emptyReads >= 100 {
			return io.ErrNoProgress
		}
	}
}
