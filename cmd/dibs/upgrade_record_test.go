package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/build"
	"github.com/agenxy/dibs/internal/selfupdate"
)

// Exercise the real upgrade dispatch/acquire/extract/place/retain/cleanup path
// in a private installation, including --allow-unsigned. No services, real
// installs or live network are touched. The stand-in cosign proves wiring,
// not cryptography (the separate real signed offline CLI probe proves that).
func TestUpgradeRetainsOnlySignatureVerifiedEvidenceThroughActualCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no published upgrade archive")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"signed", "unsigned", "cache-refuses"} {
		t.Run(mode, func(t *testing.T) {
			bin, dir := t.TempDir(), t.TempDir()
			probe := filepath.Join(bin, "dibs-upgrade-probe")
			for _, name := range []string{probe, filepath.Join(bin, "cosign")} {
				if err := os.WriteFile(name, data, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(bin, "dibd"), []byte("old fixture daemon"), 0o700); err != nil {
				t.Fatal(err)
			}
			cache := filepath.Join(dir, "guest-release.json")
			if mode == "cache-refuses" {
				if err := os.Mkdir(cache, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if mode == "unsigned" {
				if err := os.WriteFile(cache, []byte("old evidence must not change"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			args := []string{"-test.run=^TestCLIUpgradeRecordProcess$", "--", "upgrade", "--fetch", "--dry-run"}
			if mode == "unsigned" {
				args = append(args, "--allow-unsigned")
			}
			cmd := exec.CommandContext(ctx, probe, args...)
			cmd.Env = []string{"DIBS_TEST_UPGRADE_RECORD=1", "DIBS_TEST_UPGRADE_COSIGN=1", "DIBS_DIR=" + dir, "PATH=" + bin}
			for _, key := range []string{"TMPDIR", "TEMP", "SystemRoot", "SYSTEMROOT"} {
				if value, ok := os.LookupEnv(key); ok {
					cmd.Env = append(cmd.Env, key+"="+value)
				}
			}
			out, err := cmd.CombinedOutput()
			if mode == "cache-refuses" {
				if err == nil || !strings.Contains(string(out), "already installed") || !strings.Contains(string(out), "fleet has not been moved") {
					t.Fatalf("post-install evidence failure misreported: %v %s", err, out)
				}
			} else if err != nil {
				t.Fatalf("fixture upgrade failed: %v %s", err, out)
			}
			payload, err := os.ReadFile(filepath.Join(bin, "dibd"))
			if err != nil || string(payload) != "new fixture dibd" {
				t.Fatalf("setup never traversed actual install: %q %v", payload, err)
			}
			if mode == "signed" {
				b, err := os.ReadFile(cache)
				if err != nil {
					t.Fatal(err)
				}
				var record struct {
					Tag               string
					Checksums, Bundle []byte
				}
				if err = json.Unmarshal(b, &record); err != nil || record.Tag != "v0.0.9" || string(record.Checksums) != upgradeFixtureChecksums() || string(record.Bundle) != "fixture signed bundle\n" {
					t.Fatalf("exact evidence was not retained: %+v %v", record, err)
				}
			} else if mode == "unsigned" {
				b, err := os.ReadFile(cache)
				if err != nil || string(b) != "old evidence must not change" {
					t.Fatalf("unsigned install manufactured/changed provenance: %q %v", b, err)
				}
			}
			entries, err := os.ReadDir(bin)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".dibs-upgrade-") {
					t.Fatal("upgrade staging survived cleanup")
				}
			}
		})
	}
}

func upgradeFixtureArchive() []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"dibs", "dibd"} {
		content := []byte("new fixture " + name)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o700, Size: int64(len(content))}); err != nil {
			panic(err)
		}
		if _, err := tw.Write(content); err != nil {
			panic(err)
		}
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gz.Close(); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func upgradeFixtureChecksums() string {
	goos, goarch := selfupdate.Platform()
	name, err := selfupdate.ArchiveName("0.0.9", goos, goarch)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x  %s\n", sha256.Sum256(upgradeFixtureArchive()), name)
}

func fakeUpgradeCosign(args []string) int {
	if len(args) == 1 && args[0] == "version" {
		return 0
	}
	if len(args) != 8 || args[0] != "verify-blob" {
		return 20
	}
	checksums, err := os.ReadFile(args[1])
	if err != nil || string(checksums) != upgradeFixtureChecksums() {
		return 21
	}
	bundle, err := os.ReadFile(args[3])
	if err != nil || string(bundle) != "fixture signed bundle\n" || args[2] != "--bundle" ||
		args[4] != "--certificate-identity" || args[5] != "https://github.com/Agenxy/dibs/.github/workflows/release.yml@refs/tags/v0.0.9" ||
		args[6] != "--certificate-oidc-issuer" || args[7] != "https://token.actions.githubusercontent.com" {
		return 22
	}
	return 0
}

type upgradeRecordTransport struct{}

func (upgradeRecordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.Header.Get("Authorization") != "" {
		return nil, fmt.Errorf("unexpected credential-bearing fixture request")
	}
	var body string
	switch req.URL.String() {
	case "https://api.github.com/repos/Agenxy/dibs/releases/latest":
		body = `{"tag_name":"v0.0.9","html_url":"https://github.com/Agenxy/dibs/releases/tag/v0.0.9"}`
	case selfupdate.DownloadURL("v0.0.9", selfupdate.ChecksumsName):
		body = upgradeFixtureChecksums()
	case selfupdate.DownloadURL("v0.0.9", selfupdate.BundleName):
		body = "fixture signed bundle\n"
	default:
		goos, goarch := selfupdate.Platform()
		name, err := selfupdate.ArchiveName("0.0.9", goos, goarch)
		if err != nil || req.URL.String() != selfupdate.DownloadURL("v0.0.9", name) {
			return nil, fmt.Errorf("unexpected fixture request: %s", req.URL)
		}
		body = string(upgradeFixtureArchive())
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestCLIUpgradeRecordProcess(t *testing.T) {
	if os.Getenv("DIBS_TEST_UPGRADE_RECORD") != "1" {
		return
	}
	build.Version = "0.0.8" // fixture: force the actual --fetch path, not Current
	http.DefaultTransport = upgradeRecordTransport{}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"dibs"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing CLI upgrade helper argument separator")
}
