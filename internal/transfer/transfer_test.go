package transfer

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/blobstore"
	"github.com/agenxy/dibs/internal/core"
	"github.com/agenxy/dibs/internal/engine"
	"github.com/agenxy/dibs/internal/ledger"
)

func transferFixture(t *testing.T) (*Manager, string, string) {
	t.Helper()
	dir := t.TempDir()
	box, err := ledger.LoadOrCreateKey(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(filepath.Join(dir, "ledger.jsonl"), "transfer-test", box)
	if err != nil {
		t.Fatal(err)
	}
	bs, err := blobstore.New(dir, box)
	if err != nil {
		t.Fatal(err)
	}
	limits := core.DefaultLimits()
	limits.MaxBlobSize, limits.PerAgentBlobBytes, limits.BlobStoreBytes = 8, 8, 16
	e := engine.New(core.NewState("transfer-test", limits), led, nil)
	e.SetBlobs(bs)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; _ = led.Close() })
	result, err := e.Do(ctx, &core.Op{Kind: core.OpRegister, Name: "worker"})
	if err != nil || result["token"] == nil {
		t.Fatalf("register setup: %v %v", result, err)
	}
	m := New(ctx, e, bs, dir)
	m.now = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	return m, result["token"].(string), dir
}

func authorizeEight(t *testing.T, m *Manager, token string) (core.Result, error) {
	t.Helper()
	size := int64(8)
	return m.Authorize(context.Background(), "http://127.0.0.1:4777", token, "", "", "", "", &size)
}

func TestExpiryReleasesStagingDeterministically(t *testing.T) {
	m, token, dir := transferFixture(t)
	first, err := authorizeEight(t, m, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizeEight(t, m, token); !errors.Is(err, core.ErrQuota) {
		t.Fatalf("concurrent admission did not reserve bytes: %v", err)
	}
	oldNow := m.now()
	m.now = func() time.Time { return oldNow.Add(ticketLifetime) }
	m.cleanup(false)
	files, err := os.ReadDir(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), ".tmp-") {
			t.Fatal("expired encrypted staging survived")
		}
	}
	if _, err := authorizeEight(t, m, token); err != nil {
		t.Fatalf("expiry stranded a reservation: %v", err)
	}
	d := first["upload"].(Descriptor)
	r := httptest.NewRequest(http.MethodHead, d.URL, nil)
	w := httptest.NewRecorder()
	m.Handler("", false).ServeHTTP(w, r)
	if w.Code != 410 {
		t.Fatalf("expired ticket is usable: %d", w.Code)
	}
}

func TestCancelAndTerminalFailureReleaseReservation(t *testing.T) {
	for _, method := range []string{http.MethodDelete, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			m, token, _ := transferFixture(t)
			first, err := authorizeEight(t, m, token)
			if err != nil {
				t.Fatal(err)
			}
			d := first["upload"].(Descriptor)
			r := httptest.NewRequest(method, d.URL, strings.NewReader("ninebytes"))
			r.RemoteAddr = "127.0.0.1:1234"
			w := httptest.NewRecorder()
			m.Handler("", false).ServeHTTP(w, r)
			want := 204
			if method == http.MethodPut {
				want = 413
			}
			if w.Code != want {
				t.Fatalf("terminal request: %d %s", w.Code, w.Body.String())
			}
			if _, err := authorizeEight(t, m, token); err != nil {
				t.Fatalf("terminal exit stranded quota: %v", err)
			}
		})
	}
}

func TestTransferTransportRejectsOffHostAndOldTLS(t *testing.T) {
	r := httptest.NewRequest(http.MethodHead, "https://example.com/files/ticket", nil)
	r.RemoteAddr = "192.0.2.1:4444"
	r.Header.Set("X-Forwarded-Proto", "https")
	if err := transferTransport(r, "https://example.com", true); err == nil {
		t.Fatal("forwarded header admitted off-host plaintext")
	}
	r.TLS = &tls.ConnectionState{Version: tls.VersionTLS12}
	if err := transferTransport(r, "https://example.com", false); err == nil {
		t.Fatal("TLS 1.2 admitted")
	}
	r.TLS.Version = tls.VersionTLS13
	if err := transferTransport(r, "https://example.com", false); err != nil {
		t.Fatal(err)
	}
}

func TestPendingHandleDoesNotDiscloseUploadCapability(t *testing.T) {
	m, token, _ := transferFixture(t)
	r, err := authorizeEight(t, m, token)
	if err != nil {
		t.Fatal(err)
	}
	file := r["file"].(FileValue)
	d := r["upload"].(Descriptor)
	handle := strings.TrimPrefix(file.URI, "dibs:pending:")
	if handle == file.URI || len(handle) != 32 || strings.Contains(d.URL, handle) {
		t.Fatal("pending handle carries upload secret")
	}
}
