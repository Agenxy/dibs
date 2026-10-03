package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fixtureChecksums = "exact signed checksum bytes\n"
const fixtureBundle = "  {\n \"signature\": \"fixture only\"\n }\n"

// A process-door wiring test, NOT cryptographic evidence. The real signed
// v0.0.9 bundle is separately measured through the network-denied CLI.
func recordCosignHelper() int {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		return 0
	}
	if len(os.Args) < 2 || os.Args[1] != "verify-blob" || os.Getenv("TUF_MIRROR") != "" ||
		os.Getenv("TUF_ROOT_JSON") != "" || os.Getenv("TUF_ROOT") == "" {
		return 20
	}
	args := os.Args[2:]
	if len(args) != 7 && len(args) != 9 {
		return 21
	}
	checksums, err := os.ReadFile(args[0])
	if err != nil || string(checksums) != fixtureChecksums {
		return 22
	}
	flags := map[string]string{}
	for i := 1; i+1 < len(args); i += 2 {
		flags[args[i]] = args[i+1]
	}
	bundle, err := os.ReadFile(flags["--bundle"])
	if err != nil || string(bundle) != fixtureBundle ||
		flags["--certificate-identity"] != releaseIdentity("v0.0.9") ||
		flags["--certificate-oidc-issuer"] != "https://token.actions.githubusercontent.com" {
		return 23
	}
	if root := flags["--trusted-root"]; root != "" {
		data, err := os.ReadFile(root)
		if err != nil || string(data) != sigstoreTrustedRoot ||
			!strings.HasSuffix(os.Getenv("TUF_ROOT"), "unused-tuf-cache") {
			return 24
		}
	} else if !strings.HasSuffix(os.Getenv("TUF_ROOT"), "production-tuf-cache") {
		return 25
	}
	return 0
}

func useRecordCosign(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	name := "cosign"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), b, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("DIBS_TEST_COSIGN_RECORD", "1")
	t.Setenv("TUF_ROOT", "/untrusted-cache")
	t.Setenv("TUF_MIRROR", "https://untrusted.invalid")
	t.Setenv("TUF_ROOT_JSON", "/untrusted-root.json")
}

type recordTransport func(*http.Request) (*http.Response, error)

func (r recordTransport) RoundTrip(req *http.Request) (*http.Response, error) { return r(req) }

func fixtureRelease(t *testing.T) VerifiedRelease {
	t.Helper()
	useRecordCosign(t)
	var requests int
	c := &http.Client{Transport: recordTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		body := fixtureChecksums
		if req.URL.String() == DownloadURL("v0.0.9", BundleName) {
			body = fixtureBundle
		} else if req.URL.String() != DownloadURL("v0.0.9", ChecksumsName) {
			t.Fatalf("unexpected metadata request: %s", req.URL)
		}
		if req.Header.Get("Authorization") != "" {
			t.Fatal("board credentials entered release metadata fetch")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	rel, err := ReleaseForTag("v0.0.9")
	if err != nil {
		t.Fatal(err)
	}
	v, err := AcquireVerifiedRelease(context.Background(), c, rel, t.TempDir())
	if err != nil || requests != 2 || v.Tag() != rel.Tag || v.Checksums() != fixtureChecksums {
		t.Fatalf("acquisition setup failed: %+v requests=%d err=%v", v, requests, err)
	}
	return v
}

func saveFixtureRecord(t *testing.T, v VerifiedRelease, dir string) {
	t.Helper()
	err := v.Save(dir)
	// Windows is not a published guest platform; directory durability may
	// refuse there. Even that ambiguous failure must leave readable evidence.
	if err != nil && (runtime.GOOS != "windows" || !strings.Contains(err.Error(), "durability unconfirmed")) {
		t.Fatal(err)
	}
}

func TestReleaseRecordRoundTripVerifiesExactBytesAtProcessDoor(t *testing.T) {
	v := fixtureRelease(t)
	dir := t.TempDir()
	saveFixtureRecord(t, v, dir)
	got, err := LoadVerifiedRelease(context.Background(), dir, "devel")
	if err != nil || got.tag != v.tag || got.checksums != v.checksums || got.bundle != v.bundle {
		t.Fatalf("retained exact bytes did not reverify: %+v %v", got, err)
	}
	// Save reuses neither an editable authority nor the OLD embedded root.
	// A newer signature accepted by normal TUF may require a newer binary to
	// read it offline; persistence must not prevent that binary being installed.
	v.tag = "v0.0.10"
	saveFixtureRecord(t, v, dir)
	_, err = LoadVerifiedRelease(context.Background(), dir, "0.0.9")
	if err == nil || !strings.Contains(err.Error(), "newer than this dibs build") {
		t.Fatalf("newer-tag conditional failure hint missing: %v", err)
	}
	_, err = LoadVerifiedRelease(context.Background(), dir, "0.0.10")
	if err == nil || strings.Contains(err.Error(), "upgrade dibs") {
		t.Fatalf("same-version forgery diagnosed as key rotation: %v", err)
	}
}

func TestReleaseRecordCannotSupplyItsOwnAuthorityOrChangeSignedBytes(t *testing.T) {
	v := fixtureRelease(t)
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"checksum", func(r map[string]any) { r["checksums"] = []byte("altered") }},
		{"bundle", func(r map[string]any) { r["bundle"] = []byte("altered") }},
		{"tag", func(r map[string]any) { r["tag"] = "v0.0.10" }},
		{"verified-flag", func(r map[string]any) { r["verified"] = true }},
		{"editable-root", func(r map[string]any) { r["trusted_root"] = "forged root" }},
		{"missing-bundle", func(r map[string]any) { delete(r, "bundle") }},
		{"oversized-checksums", func(r map[string]any) { r["checksums"] = []byte(strings.Repeat("x", maxChecksums+1)) }},
		{"alias", func(r map[string]any) { r["tag"] = "latest" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r := map[string]any{"tag": v.tag, "checksums": []byte(v.checksums), "bundle": []byte(v.bundle)}
			tc.edit(r)
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, releaseRecordName), b, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err = LoadVerifiedRelease(context.Background(), dir, "devel"); err == nil {
				t.Fatal("untrusted record was admitted")
			}
		})
	}
}

func TestReleaseRecordBoundedMissingAndNonregular(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadVerifiedRelease(context.Background(), dir, "devel"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing evidence lost its distinct error: %v", err)
	}
	for _, body := range []string{"{} {}", "{} garbage", strings.Repeat("x", maxReleaseRecord+1)} {
		if err := os.WriteFile(filepath.Join(dir, releaseRecordName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadVerifiedRelease(context.Background(), dir, "devel"); err == nil {
			t.Fatal("invalid or oversized record accepted")
		}
	}
	if err := (VerifiedRelease{}).Save(dir); err == nil {
		t.Fatal("zero-value/unsigned record saved")
	}
	if runtime.GOOS == "windows" {
		return // symlink privileges are not guaranteed on this unsupported target
	}
	linkDir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, releaseRecordName), filepath.Join(linkDir, releaseRecordName)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVerifiedRelease(context.Background(), linkDir, "devel"); err == nil {
		t.Fatal("symlink record accepted")
	}
	v := fixtureRelease(t)
	if err := v.Save(linkDir); err == nil {
		t.Fatal("symlink destination overwritten")
	}
}

func TestReleaseEvidenceDownloadsAreBoundedBeforeFilesAreWritten(t *testing.T) {
	c := &http.Client{Transport: recordTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxChecksums+1)))}, nil
	})}
	dir := t.TempDir()
	err := saveEvidence(context.Background(), c, DownloadURL("v0.0.9", ChecksumsName), filepath.Join(dir, ChecksumsName), maxChecksums)
	if err == nil {
		t.Fatal("oversized metadata saved")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("oversized fetch wrote staging bytes: %v %v", entries, err)
	}
}

func TestReleaseTagsAreStableExactAndURLSafe(t *testing.T) {
	for _, tag := range []string{"latest", "0.0.9", "v00.0.9", "v+1.2.3", "v1.2.3+build", "v1.2.3-rc", "v1.2.3/path", "v1.2.3\n"} {
		if _, err := ReleaseForTag(tag); err == nil {
			t.Errorf("noncanonical tag accepted: %q", tag)
		}
	}
}
