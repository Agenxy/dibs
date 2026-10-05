// Temporary, never-merged hosted stamper receipt. Production tools are unchanged.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "release-preparation:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 4 {
		return fmt.Errorf("want source, artifact directory and exact stub SHA")
	}
	source, err := filepath.Abs(os.Args[1])
	if err != nil {
		return err
	}
	artifact, err := filepath.Abs(os.Args[2])
	if err != nil {
		return err
	}
	if err := os.Mkdir(artifact, 0o700); err != nil {
		return err
	}
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git %v: %w", args, err)
		}
		return out, nil
	}
	write := func(name string, data []byte) error {
		return os.WriteFile(filepath.Join(artifact, name), data, 0o600)
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != os.Args[3] {
		return fmt.Errorf("source HEAD differs from exact stub")
	}
	status, err := git("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if len(status) != 0 {
		return fmt.Errorf("stub source is not clean: %s", status)
	}
	tree, err := git("rev-parse", "HEAD^{tree}")
	if err != nil {
		return err
	}

	// One unchanged command. Its first task authenticates current TUF trust;
	// Task's failure propagation prevents stamping if that check fails.
	cmd := exec.Command("task", "release", "VERSION=0.0.12")
	cmd.Dir = source
	out, taskErr := cmd.CombinedOutput()
	fmt.Print(string(out))
	if err := write("task-release.log", out); err != nil {
		return err
	}
	if taskErr != nil {
		return fmt.Errorf("unchanged task release failed: %w", taskErr)
	}
	rootMarker := []byte("sigstore-root-check: authenticated current root matches embedded pin")
	stampMarker := "task: [release] go run ./tools/version -set 0.0.12"
	cut := strings.Index(string(out), stampMarker)
	if cut < 0 || !strings.Contains(string(out[:cut]), string(rootMarker)) {
		return fmt.Errorf("successful task output omitted root-check/stamp boundary")
	}
	if err := write("root-check.log", out[:cut]); err != nil {
		return err
	}
	if err := write("stamp.log", out[cut:]); err != nil {
		return err
	}
	files, err := git("diff", "--name-only")
	if err != nil {
		return err
	}
	got := strings.Fields(string(files))
	want := []string{
		"CHANGELOG.md", "server.json",
		"plugins/claude-code/.claude-plugin/plugin.json",
		"internal/plugins/data/claude-code/.claude-plugin/plugin.json",
		"plugins/codex/.codex-plugin/plugin.json",
		"internal/plugins/data/codex/.codex-plugin/plugin.json",
		"plugins/claude-desktop/manifest.json",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		return fmt.Errorf("stamp changed unexpected files: %v", got)
	}
	patch, err := git("diff", "--binary", "--full-index", "--no-color", "--no-ext-diff", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/")
	if err != nil {
		return err
	}
	if err := write("generated.patch", patch); err != nil {
		return err
	}
	proof, err := json.MarshalIndent(map[string]any{
		"source_sha": strings.TrimSpace(string(head)),
		"source_tree": strings.TrimSpace(string(tree)),
		"command": []string{"task", "release", "VERSION=0.0.12"},
		"task_exit": 0,
		"version": "0.0.12",
		"patch_sha256": fmt.Sprintf("%x", sha256.Sum256(patch)),
		"changed_files": got,
		"github_run_id": os.Getenv("GITHUB_RUN_ID"),
		"github_run_attempt": os.Getenv("GITHUB_RUN_ATTEMPT"),
		"github_workflow_sha": os.Getenv("GITHUB_SHA"),
	}, "", "  ")
	if err != nil {
		return err
	}
	return write("provenance.json", append(proof, '\n'))
}
