package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agenxy/dibs/internal/build"
)

// Enter the real invite handler with unusable board configuration. The floor
// refusal must win before authentication, minting, network or cache writes.
func TestInviteVerifyReleaseRefusesUnsetMinimumBeforeIO(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DIBS_DIR", dir)
	t.Setenv("DIBS_TOKEN", "not-a-real-token")
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("broken = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	err := inviteCmd([]string{"--verify-release", "v0.0.9"})
	if err == nil || !strings.Contains(err.Error(), "supporting guest release minimum is unset") {
		t.Fatalf("expected pre-I/O release-floor refusal, got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("verification refusal changed the board: %v %v", entries, err)
	}
}

// Opt-in measured crypto probe. CI's stand-in proves wiring, not signatures.
// Run this compiled test binary under a network-denying OS policy with the
// public v0.0.9 checksum/bundle fixture and a real, pinned cosign on PATH.
func TestGuestReleaseCLIRealSignedFixture(t *testing.T) {
	fixture := os.Getenv("DIBS_TEST_SIGNED_RELEASE_FIXTURE_DIR")
	if fixture == "" {
		t.Skip("requires public signed fixture and real cosign; not a synthetic crypto proof")
	}
	checksums, err := os.ReadFile(filepath.Join(fixture, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(filepath.Join(fixture, "checksums.txt.bundle"))
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tag string
		want      bool
	}{
		{"exact", "v0.0.9", true},
		{"tampered", "v0.0.9", false},
		{"wrong-tag", "v0.0.10", false},
		{"editable-root", "v0.0.9", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			r := map[string]any{"tag": tc.tag, "checksums": checksums, "bundle": bundle}
			if tc.name == "tampered" {
				r["checksums"] = append(append([]byte(nil), checksums...), '\n')
			}
			if tc.name == "editable-root" {
				r["trusted_root"] = "not authority"
			}
			body, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "guest-release.json"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestCLIReleaseEvidenceProcess$", "--", "invite", "--verify-release", tc.tag)
			cmd.Env = []string{"DIBS_TEST_RELEASE_EVIDENCE=1", "DIBS_DIR=" + dir}
			for _, key := range []string{"PATH", "TMPDIR", "TEMP", "SystemRoot", "SYSTEMROOT"} {
				if val, ok := os.LookupEnv(key); ok {
					cmd.Env = append(cmd.Env, key+"="+val)
				}
			}
			out, err := cmd.CombinedOutput()
			t.Logf("%s: exit=%v output=%s", tc.name, err, out)
			if (err == nil) != tc.want {
				t.Fatalf("real CLI signature result: got %v want success=%t: %s", err, tc.want, out)
			}
			if tc.want && (!strings.Contains(string(out), "actual board build remains devel") || !strings.Contains(string(out), "INCOMPLETE")) {
				t.Fatal("fixture proof relabelled the devel board or claimed guest readiness")
			}
			if !tc.want && (strings.Contains(string(out), "fetching") || strings.Contains(string(out), "Get \"https://")) {
				t.Fatal("untrusted cache triggered network fallback")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("offline read initialized or changed the board: %v %v", entries, err)
			}
		})
	}
}

func TestCLIReleaseEvidenceProcess(t *testing.T) {
	if os.Getenv("DIBS_TEST_RELEASE_EVIDENCE") != "1" {
		return
	}
	// TEST-ONLY fixture floor, not a change to production or a supported tag.
	guestSupportingMinimum = "v0.0.9"
	build.Version = "devel"
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"dibs"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing CLI helper argument separator")
}

func TestInviteReleaseCannotBeMixedWithMintOrUnsigned(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"worker", "--verify-release", "v0.0.9"},
		{"--verify-release", "v0.0.9", "worker"},
		{"--verify-release", "v0.0.9", "--ttl", "1d"},
		{"--verify-release", "v0.0.9", "--out", "/tmp/recipe"},
		{"--verify-release", "v0.0.9", "--allow-unsigned"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, exe, append([]string{"-test.run=^TestCLIHelpProcess$", "--", "invite"}, args...)...)
		cmd.Env = []string{"DIBS_TEST_CLI_HELP=1", "DIBS_DIR=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		cancel()
		if err == nil {
			t.Fatalf("ambiguous CLI invocation accepted: %v: %s", args, out)
		}
	}
}

func TestGuestReleaseSelectionUsesCompiledFloorWithoutRelabellingDevel(t *testing.T) {
	for _, tc := range []struct {
		tag, minimum, board string
		want                bool
	}{
		{"v0.0.9", "", "devel", false},
		{"v0.0.8", "v0.0.9", "devel", false},
		{"v0.0.9", "v0.0.9", "devel", true},
		{"v0.0.9", "v0.0.9", "0.0.10", false},
		{"v0.0.10", "v0.0.9", "0.0.10", true},
		{"v0.0.11", "v0.0.9", "0.0.10", true},
		{"v0.0.9", "v0.0.9", "0.0.10-0.revision", true},
		{"latest", "v0.0.9", "devel", false},
		{"v0.0.9", "invalid", "devel", false},
	} {
		got, err := guestReleaseSelection(tc.tag, tc.minimum, tc.board)
		if (err == nil) != tc.want || (tc.want && got.Tag != tc.tag) {
			t.Errorf("%+v: got %+v %v", tc, got, err)
		}
	}
}
