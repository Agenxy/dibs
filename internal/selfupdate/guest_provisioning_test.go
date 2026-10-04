package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGuestProvisioningNeedsSnapshotAndCanonicalProjection(t *testing.T) {
	if _, err := (GuestReleaseSnapshot{}).Provisioning(); err == nil {
		t.Fatal("zero snapshot offered provisioning")
	}
	snapshot, err := (VerifiedRelease{tag: "v0.0.9", checksums: guestFixtureChecksums()}).GuestSnapshot("v0.0.9", "devel+actual")
	if err != nil {
		t.Fatal(err)
	}
	p, err := snapshot.Provisioning()
	if err != nil || p.Validate(snapshot.Metadata()) != nil || p.Status != "INCOMPLETE" || len(p.Targets) != 3 {
		t.Fatal("canonical snapshot projection failed:", err)
	}
	if p.Checksums != guestFixtureChecksums() {
		t.Fatal("private handoff digests differ from exact signed lines")
	}
	for _, edit := range []func(*GuestProvisioning){
		func(p *GuestProvisioning) { p.Targets[0].Steps[1].Argv[0] = "evil" },
		func(p *GuestProvisioning) { p.Targets[0].Steps[1].Command = "sudo installer" },
		func(p *GuestProvisioning) { p.Targets[0].MCP.Args[0] = "dibd" },
		func(p *GuestProvisioning) { p.Targets[0].OS = "windows" },
		func(p *GuestProvisioning) { p.Checksums += "invented\n" },
		func(p *GuestProvisioning) { p.Status = "READY" },
	} {
		p, err := snapshot.Provisioning()
		if err != nil {
			t.Fatal(err)
		}
		edit(&p)
		if p.Validate(snapshot.Metadata()) == nil {
			t.Fatal("edited instructions admitted")
		}
	}
	if fresh, _ := snapshot.Provisioning(); fresh.Validate(snapshot.Metadata()) != nil {
		t.Fatal("caller edits corrupted frozen snapshot")
	}
}

// Drive the actual rendered argument vectors with native primitives, not a
// shell script or a mirrored extraction helper. Public downloading/signature
// cryptography have their own doors: here fixture archive bytes and admitted
// digests discriminate hash-before-extract, stdout-only writes and publication.
func TestGuestProvisioningLiteralPrimitivesStopBeforeUse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no published Windows guest target")
	}
	for _, tool := range []string{"mktemp", "grep", "tar", "mkdir", "chmod", "ln"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatal("required fixture primitive unavailable:", tool, err)
		}
	}
	for _, mode := range []string{"good", "archive mismatch", "missing checksum", "member mismatch", "symlink member", "duplicate member", "existing version", "late competitor", "late directory", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			payload := []byte("exact packaged executable fixture; never executed\n")
			var compressed bytes.Buffer
			gz := gzip.NewWriter(&compressed)
			tw := tar.NewWriter(gz)
			write := func(name string, typ byte, data []byte) {
				t.Helper()
				h := &tar.Header{Name: name, Mode: 0o700, Typeflag: typ, Size: int64(len(data))}
				if typ == tar.TypeSymlink {
					h.Linkname, h.Size = "../../outside", 0
					data = nil
				}
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write(data); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink member" {
				write("dibs", tar.TypeSymlink, nil)
			} else {
				if mode == "duplicate member" {
					write("dibs", tar.TypeReg, []byte("wrong duplicate"))
				}
				write("dibs", tar.TypeReg, payload)
			}
			// A correctly hashed archive may contain unrelated unsafe names;
			// stdout-only exact-member extraction must not create their paths.
			write("../outside", tar.TypeReg, []byte("must not be written"))
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			digest := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
			sums := guestFixtureChecksums()
			archiveSHA, binarySHA := digest(compressed.Bytes()), digest(payload)
			if mode == "member mismatch" {
				binarySHA = strings.Repeat("0", 64)
			}
			sums = strings.ReplaceAll(sums, strings.Repeat("a", 64), archiveSHA)
			sums = strings.ReplaceAll(sums, strings.Repeat("b", 64), binarySHA)
			snapshot, err := (VerifiedRelease{tag: "v0.0.9", checksums: sums}).GuestSnapshot("v0.0.9", "devel+fixture")
			if err != nil {
				t.Fatal(err)
			}
			p, err := snapshot.Provisioning()
			if err != nil {
				t.Fatal(err)
			}
			target := p.Targets[1]
			if runtime.GOOS == "darwin" {
				target = p.Targets[0]
			}
			if _, err := exec.LookPath(target.Steps[3].Argv[0]); err != nil {
				t.Fatal("hash fixture primitive unavailable:", err)
			}
			parent := filepath.Join(t.TempDir(), "v0.0.9")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			platform := target.OS + "_" + target.Arch
			installed := filepath.Join(parent, platform, "dibs")
			if mode == "existing version" {
				if err := os.Mkdir(filepath.Dir(installed), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(installed, []byte("retained"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			staging, stopped := "", -1
			for i, step := range target.Steps {
				if i == 1 { // public download is isolated from this local fixture
					archive := compressed.Bytes()
					if mode == "archive mismatch" {
						archive = append(append([]byte(nil), archive...), byte(0))
					}
					if err := os.WriteFile(filepath.Join(staging, step.Argv[len(step.Argv)-2]), archive, 0o600); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if mode == "interrupted" && i == 8 {
					stopped = i
					break
				}
				if mode == "late competitor" && i == 10 {
					if err := os.WriteFile(installed, []byte("retained"), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "late directory" && i == 10 {
					if err := os.Mkdir(installed, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(installed, "retained"), []byte("retained"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				replace := func(s string) string {
					return strings.ReplaceAll(strings.ReplaceAll(s, "<absolute-version-parent>", parent), "<staging>", staging)
				}
				argv := make([]string, len(step.Argv))
				for j, arg := range step.Argv {
					argv[j] = replace(arg)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
				cmd.Dir = replace(step.Cwd)
				var output bytes.Buffer
				cmd.Stdout, cmd.Stderr = &output, &output
				var file *os.File
				if step.StdoutFile != "" {
					file, err = os.OpenFile(filepath.Join(cmd.Dir, step.StdoutFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
					if err != nil {
						t.Fatal(err)
					}
					cmd.Stdout = file
				}
				err = cmd.Run()
				cancel()
				if file != nil {
					if closeErr := file.Close(); closeErr != nil {
						t.Fatal(closeErr)
					}
				}
				if err != nil {
					stopped = i
					t.Logf("actual primitive stopped at step%d: %v %s", i, err, output.Bytes())
					break
				}
				if i == 0 {
					staging = strings.TrimSpace(output.String())
					if !filepath.IsAbs(staging) || filepath.Dir(staging) != parent {
						t.Fatal("mktemp fixture setup failed")
					}
					checksums := p.Checksums
					if mode == "missing checksum" {
						checksums = ""
					}
					if err := os.WriteFile(filepath.Join(staging, "checksums.txt"), []byte(checksums), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			wantStop := map[string]int{"good": -1, "archive mismatch": 3, "missing checksum": 2, "member mismatch": 7, "duplicate member": 7, "existing version": 9, "late competitor": 10, "late directory": 10, "interrupted": 8}
			if mode == "symlink member" {
				if stopped != 5 && stopped != 7 {
					t.Fatalf("link member reached use: stopped%d", stopped)
				}
			} else if stopped != wantStop[mode] {
				t.Fatalf("wrong discriminator: stopped%d want%d", stopped, wantStop[mode])
			}
			body, readErr := os.ReadFile(installed)
			switch mode {
			case "good":
				if readErr != nil || !bytes.Equal(body, payload) {
					t.Fatal("verified publication failed")
				}
			case "existing version", "late competitor":
				if readErr != nil || string(body) != "retained" {
					t.Fatal("existing executable was overwritten")
				}
			case "late directory":
				if sentinel, err := os.ReadFile(filepath.Join(installed, "retained")); err != nil || string(sentinel) != "retained" {
					t.Fatal("competing directory was changed")
				}
				if _, err := os.Stat(filepath.Join(installed, "dibs")); !os.IsNotExist(err) {
					t.Fatal("publication silently nested inside competing directory")
				}
			default:
				if !os.IsNotExist(readErr) {
					t.Fatal("failed/interrupted checks published executable")
				}
			}
			if _, err := os.Stat(filepath.Join(parent, "outside")); !os.IsNotExist(err) {
				t.Fatal("archive path was extracted")
			}
		})
	}
}
