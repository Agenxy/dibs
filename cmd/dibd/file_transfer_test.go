package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This enters at the production public listener, not a hand-wired transfer
// manager. A cloud client must never need a hub path or the board secret.
func TestPublicFileTransferRunsThroughProductionListener(t *testing.T) {
	f := newCloudFixture(t)
	token := cloudRegistered(f)
	plain := bytes.Repeat([]byte("streamed artifact\x00"), 16000)
	sum := sha256.Sum256(plain)
	want := "sha256:" + hex.EncodeToString(sum[:])
	r := f.tool(true, "upload", map[string]any{
		"token": token, "size": len(plain), "sha256": hex.EncodeToString(sum[:]),
		"mime": "application/octet-stream", "name": "artifact.bin",
	})
	upload, ok := r["upload"].(map[string]any)
	if !ok || upload["transport"] != "https" || upload["method"] != "PUT" {
		t.Fatalf("production upload descriptor missing: %v", r)
	}
	url, _ := upload["url"].(string)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.public.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var stored map[string]any
	if err = json.NewDecoder(resp.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || stored["blob"] != want {
		t.Fatalf("ticket PUT: %d %v", resp.StatusCode, stored)
	}
	r = f.tool(true, "download", map[string]any{"token": token, "blob": want})
	download, ok := r["download"].(map[string]any)
	if !ok {
		t.Fatalf("production download descriptor missing: %v", r)
	}
	url, _ = download["url"].(string)
	req, err = http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=65530-131100")
	got, err := f.public.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = got.Body.Close() }()
	assertDownloadIsData(t, got, "application/octet-stream")
	part, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got.StatusCode != http.StatusPartialContent || !bytes.Equal(part, plain[65530:131101]) {
		t.Fatalf("Range did not cross authenticated chunks correctly: %d %d bytes", got.StatusCode, len(part))
	}
	if got.Header.Get("ETag") != `"`+want+`"` {
		t.Fatalf("wrong content-addressed ETag: %q", got.Header.Get("ETag"))
	}
	head, err := f.public.Client().Head(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = head.Body.Close() }()
	assertDownloadIsData(t, head, "application/octet-stream")
}

func authorizeTestUpload(f *cloudFixture, token string, plain []byte) string {
	return authorizeTestUploadWithMime(f, token, plain, "")
}

func authorizeTestUploadWithMime(f *cloudFixture, token string, plain []byte, mime string) string {
	f.t.Helper()
	sum := sha256.Sum256(plain)
	r := f.tool(true, "upload", map[string]any{
		"token": token, "size": len(plain), "sha256": hex.EncodeToString(sum[:]), "mime": mime,
	})
	d, ok := r["upload"].(map[string]any)
	if !ok {
		f.t.Fatalf("upload admission: %v", r)
	}
	return d["url"].(string)
}

func assertDownloadIsData(t *testing.T, response *http.Response, mime string) {
	t.Helper()
	if !strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment; filename*=UTF-8''") {
		t.Error("file can render inline on the board origin")
	}
	if response.Header.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
		t.Error("file response lacks an opaque-origin sandbox")
	}
	if response.Header.Get("Content-Type") != mime || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("file response can execute or sniff its declared active content type")
	}
}

func TestPublicDownloadTreatsActiveContentAsData(t *testing.T) {
	f := newCloudFixture(t)
	token := cloudRegistered(f)
	for _, mime := range []string{
		"text/html", "image/svg+xml", "application/xhtml+xml", "text/xml",
		"application/atom+xml", "text/javascript", "Text/HTML", "text/plain",
	} {
		t.Run(mime, func(t *testing.T) {
			plain := []byte(mime + "<script>localStorage.getItem('page_key')</script>")
			target := authorizeTestUploadWithMime(f, token, plain, mime)
			stored := patchTestUpload(t, f.public.Client(), target, 0, plain, "?1")
			defer func() { _ = stored.Body.Close() }()
			var result map[string]any
			if err := json.NewDecoder(stored.Body).Decode(&result); err != nil || stored.StatusCode != 200 {
				t.Fatalf("upload setup: %d %v", stored.StatusCode, err)
			}
			meta := f.tool(true, "download", map[string]any{"token": token, "blob": result["blob"]})
			file, ok := meta["file"].(map[string]any)
			if !ok || file["mimeType"] != result["mime"] {
				t.Fatalf("declared MIME lost from JSON: %v", meta)
			}
			download := meta["download"].(map[string]any)
			response, err := f.public.Client().Get(download["url"].(string))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			want := "application/octet-stream"
			if mime == "text/plain" {
				want = mime
			}
			assertDownloadIsData(t, response, want)
			got, err := io.ReadAll(response.Body)
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("download changed file bytes: %v", err)
			}
		})
	}
}

func patchTestUpload(t *testing.T, client *http.Client, target string, offset int64, plain []byte, complete string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, target, bytes.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/partial-upload")
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	req.Header.Set("Upload-Complete", complete)
	req.Header.Set("Upload-Draft-Interop-Version", "9")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestPublicTransferResumesAfterBrokenConnection(t *testing.T) {
	f := newCloudFixture(t)
	token := cloudRegistered(f)
	plain := bytes.Repeat([]byte("resumable encrypted artifact"), 9000)
	target := authorizeTestUpload(f, token, plain)
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := f.public.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("test TLS client missing")
	}
	conn, err := tls.Dial("tcp", u.Host, transport.TLSClientConfig)
	if err != nil {
		t.Fatal(err)
	}
	// The byte request is cut, not cancelled or falsely marked incomplete.
	// Its declared request body is longer than the bytes delivered on the wire.
	_, err = fmt.Fprintf(conn, "PATCH %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/partial-upload\r\nUpload-Offset: 0\r\nUpload-Complete: ?1\r\nContent-Length: %d\r\n\r\n", u.Path, u.Host, len(plain))
	if err != nil {
		t.Fatal(err)
	}
	const sent = 75001
	if _, err = conn.Write(plain[:sent]); err != nil {
		t.Fatal(err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	var offset int64
	for range 100 {
		req, err := http.NewRequest(http.MethodHead, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := f.public.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			offset, err = strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Header.Get("Upload-Complete") != "?0" {
				t.Fatal("cut request was finalized")
			}
			if offset > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if offset <= 0 || offset > sent {
		t.Fatalf("lost accepted offset: %d", offset)
	}
	wrong := patchTestUpload(t, f.public.Client(), target, 0, plain, "?1")
	defer func() { _ = wrong.Body.Close() }()
	if wrong.StatusCode != 409 || wrong.Header.Get("Upload-Offset") != strconv.FormatInt(offset, 10) {
		t.Fatal("offset conflict failed to report current state")
	}
	partial := patchTestUpload(t, f.public.Client(), target, offset, plain[offset:offset+31], "?0")
	defer func() { _ = partial.Body.Close() }()
	if partial.StatusCode != 204 || partial.Header.Get("Upload-Complete") != "?0" {
		t.Fatal("explicit incomplete PATCH was finalized")
	}
	offset += 31
	good := patchTestUpload(t, f.public.Client(), target, offset, plain[offset:], "?1")
	defer func() { _ = good.Body.Close() }()
	var result map[string]any
	if err := json.NewDecoder(good.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plain)
	if good.StatusCode != 200 || result["blob"] != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("resume: %d %v", good.StatusCode, result)
	}
	replayed := patchTestUpload(t, f.public.Client(), target, int64(len(plain)), nil, "?1")
	defer func() { _ = replayed.Body.Close() }()
	if replayed.StatusCode != 200 || replayed.Header.Get("Upload-Complete") != "?1" {
		t.Fatal("lost final response cannot be recovered")
	}
}

func TestPublicTransferRevocationAndIntegrity(t *testing.T) {
	for _, mode := range []string{"revoke", "hash", "cancel", "over-limit"} {
		t.Run(mode, func(t *testing.T) {
			f := newCloudFixture(t)
			token := cloudRegistered(f)
			plain := []byte("expected artifact")
			target := authorizeTestUpload(f, token, plain)
			if mode == "revoke" {
				if code, r := f.admin("revoke", "cloud-worker"); code != 200 {
					t.Fatalf("revoke setup: %d %v", code, r)
				}
			}
			if mode == "cancel" {
				req, err := http.NewRequest(http.MethodDelete, target, nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := f.public.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != 204 {
					t.Fatal("cancel refused")
				}
			}
			body := plain
			if mode == "hash" {
				body = []byte("altered! artifact")
			}
			if mode == "over-limit" {
				body = append(append([]byte(nil), plain...), 1)
			}
			resp := patchTestUpload(t, f.public.Client(), target, 0, body, "?1")
			defer func() { _ = resp.Body.Close() }()
			want := map[string]int{"revoke": 403, "hash": 422, "cancel": 410, "over-limit": 413}[mode]
			if resp.StatusCode != want {
				data, _ := io.ReadAll(resp.Body)
				t.Fatalf("failure = %d want %d: %s", resp.StatusCode, want, data)
			}
		})
	}
}
