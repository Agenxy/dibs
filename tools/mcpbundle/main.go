// mcpbundle packs the released binaries into an MCP Bundle (.mcpb), so the
// registry entry can carry a machine-readable install path (issue #44).
//
// An agent that finds Dibs in the MCP registry found, until this existed,
// a name and a repository and nothing that said what to fetch or how to
// launch it; the third-party aggregator that outranked the repository in
// search became the de facto install documentation. The registry's package
// types are npm, PyPI, OCI and mcpb, and the only one a Go binary shipped
// through GitHub Releases fits is mcpb: a zip with a manifest.json and the
// server inside it.
//
// One bundle, one platform: macOS on Apple silicon, the only macOS build
// Dibs ships. A manifest selects a binary by operating system
// (`platform_overrides`, keyed win32/darwin/linux) and not by architecture,
// so a bundle that carried Linux would hand every Linux host the same
// binary, and the first version did exactly that: it shipped linux amd64
// under the `linux` override and described Linux arm64 as "not in it", which
// is not what an arm64 host would have experienced. It would have been
// handed an amd64 binary that does not run, from a manifest whose
// compatibility list said linux. The Codex review of #109 read the two
// against each other. Linux uses the release archives, and the manifest's
// compatibility list says darwin and nothing else, so a Linux or Windows
// host is told before it installs it. What a manifest cannot say is
// "Apple silicon": Dibs ships no Intel Mac build (README, "Apple silicon
// only on the Mac"), so an Intel Mac that installs this bundle gets a binary
// it cannot run, and the only place that can be said is the description,
// where it is. The second review asked whether removing Linux had removed
// the architecture mismatch; it had not, and this is the honest remainder.
//
// Fixed output names, deliberately: the release uploads dist/dibs.mcpb and
// dist/dibs.mcpb.sha256, and the registry identifier is the release asset
// URL, which carries the tag, so the name need not.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A platform's binaries inside the bundle: where they came from in dist/
// (a glob, because GoReleaser suffixes the directory with the arch
// version) and where they go.
type platform struct {
	os, arch string
	glob     string
}

var platforms = []platform{
	{"darwin", "arm64", "dibs_darwin_arm64*"},
}

func main() {
	dist := flag.String("dist", "dist", "GoReleaser's output directory")
	version := flag.String("version", "", "the version to stamp (defaults to GITHUB_REF_NAME without its v)")
	out := flag.String("out", "", "the bundle to write (default <dist>/dibs.mcpb)")
	upload := flag.Bool("upload", false, "attach the bundle and its .sha256 to the GitHub release for the tag")
	helpers := flag.String("helpers", ".",
		"the directory holding dibs-presence and Dibs.app (the repository root after GoReleaser's hooks)")
	flag.Parse()
	if *version == "" {
		*version = strings.TrimPrefix(os.Getenv("GITHUB_REF_NAME"), "v")
	}
	if *out == "" {
		*out = filepath.Join(*dist, "dibs.mcpb")
	}
	sum, err := BuildWithHelpers(*dist, *helpers, *version, *out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcpbundle:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (sha256 %s)\n", *out, sum)
	if *upload {
		if err := attach(*out); err != nil {
			fmt.Fprintln(os.Stderr, "mcpbundle:", err)
			os.Exit(1)
		}
	}
}

// attach uploads the bundle and its digest to the release GoReleaser just
// published for this tag. From Go rather than a `run:` line, because the
// tag arrives through the environment and this repository's workflows
// carry no shell.
func attach(bundle string) error {
	tag := os.Getenv("GITHUB_REF_NAME")
	if tag == "" || os.Getenv("GITHUB_REF_TYPE") != "tag" {
		return errors.New("-upload only makes sense on a tag build")
	}
	// #nosec G204 G702 -- argv, no shell: the tag is the workflow's own ref
	// name and the paths are this tool's own output.
	cmd := exec.Command("gh", "release", "upload", tag, bundle, bundle+".sha256", "--clobber")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh release upload: %w", err)
	}
	fmt.Printf("attached %s and its digest to release %s\n", filepath.Base(bundle), tag)
	return nil
}

// Build writes the bundle and its .sha256 beside it, and returns the digest.
//
// Assembled in a temporary file beside the output and moved into place only
// once complete, so a failed build leaves no half-written bundle at the name
// the release uploads, and an earlier bundle at that name outlives a failed
// rebuild together with its digest. The first version created the output
// first and returned from every failure with the file open and truncated.
func Build(dist, version, out string) (string, error) {
	return BuildWithHelpers(dist, filepath.Dir(dist), version, out)
}

// BuildWithHelpers is Build with the directory holding the macOS helpers
// (dibs-presence and Dibs.app, built by GoReleaser's hooks in the repository
// root, which is dist's parent in the release job).
func BuildWithHelpers(dist, helpers, version, out string) (string, error) {
	if version == "" {
		return "", errors.New("no version: pass -version or run on a tag")
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), ".mcpbundle-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	if err := assemble(tmp, dist, helpers, version); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return "", err
	}
	if err := os.Chmod(out, 0o644); err != nil { // #nosec G302 -- a published archive
		return "", err
	}
	sum, err := digest(out)
	if err != nil {
		return "", err
	}
	digestLine := []byte(sum + "  " + filepath.Base(out) + "\n")
	if err := os.WriteFile(out+".sha256", digestLine, 0o644); err != nil { // #nosec G306 -- a published checksum
		return "", err
	}
	return sum, nil
}

// helperFiles are the macOS helpers that have to sit BESIDE the binaries:
// internal/humanauth finds dibs-presence next to the executable and
// internal/notify finds Dibs.app there. Round seven of the pre-release
// review: the bundle shipped the two binaries alone, so a daemon started
// from it had no Touch ID and fell back to Script Editor's name for
// notifications, which is the failure the release archives already carry
// these to prevent.
var helperFiles = []string{
	"dibs-presence",
	"Dibs.app/Contents/Info.plist",
	"Dibs.app/Contents/MacOS/dibs-notify",
}

// assemble writes the zip: the manifest, the README, every platform's two
// binaries, and the macOS helpers beside them, refusing a dist with any of
// them missing.
func assemble(f *os.File, dist, helpers, version string) error {
	zw := zip.NewWriter(f)
	if err := addFile(zw, "manifest.json", 0o644, Manifest(version)); err != nil {
		return err
	}
	if err := addFile(zw, "README.md", 0o644, []byte(readme)); err != nil {
		return err
	}
	for _, p := range platforms {
		for _, bin := range []string{"dibs", "dibd"} {
			src, err := find(dist, strings.Replace(p.glob, "dibs_", bin+"_", 1), bin)
			if err != nil {
				return err
			}
			// #nosec G304 G702 -- a path under dist/, found by a fixed glob; nothing
			// an agent wrote reaches this tool.
			body, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if err := addFile(zw, "server/"+p.os+"-"+p.arch+"/"+bin, 0o755, body); err != nil {
				return err
			}
		}
		if p.os != "darwin" {
			continue
		}
		if err := addHelpers(zw, helpers, "server/"+p.os+"-"+p.arch+"/"); err != nil {
			return err
		}
	}
	return zw.Close()
}

// addHelpers copies the macOS helpers, and everything else under Dibs.app
// (its signature, its icon), beside the binaries.
func addHelpers(zw *zip.Writer, helpers, prefix string) error {
	for _, rel := range helperFiles {
		if _, err := os.Stat(filepath.Join(helpers, rel)); err != nil {
			return fmt.Errorf("%s is not under %s: the bundle must carry the helpers the "+
				"binaries look for beside themselves, so build them first (GoReleaser's hooks do)", rel, helpers)
		}
	}
	if err := addOne(zw, filepath.Join(helpers, "dibs-presence"), prefix+"dibs-presence", 0o755); err != nil {
		return err
	}
	root := filepath.Join(helpers, "Dibs.app")
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(helpers, path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.Contains(rel, "/MacOS/") {
			mode = 0o755
		}
		return addOne(zw, path, prefix+filepath.ToSlash(rel), mode)
	})
}

func addOne(zw *zip.Writer, src, name string, mode os.FileMode) error {
	body, err := os.ReadFile(src) // #nosec G304 -- a helper under the directory the caller named
	if err != nil {
		return err
	}
	return addFile(zw, name, mode, body)
}

func find(dist, dirGlob, bin string) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(dist, dirGlob, bin))
	if len(matches) != 1 {
		return "", fmt.Errorf("want exactly one %s under %s/%s, found %d: run GoReleaser first",
			bin, dist, dirGlob, len(matches))
	}
	return matches[0], nil
}

func addFile(zw *zip.Writer, name string, mode os.FileMode, body []byte) error {
	h := &zip.FileHeader{Name: name, Method: zip.Deflate}
	h.SetMode(mode) // the executable bit travels in the zip's external attributes
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func digest(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- the bundle just written
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Manifest is the MCPB manifest for one version. Field names and the
// `${__dirname}` substitution are the bundle spec's (manifest_version 0.2).
func Manifest(version string) []byte {
	m := map[string]any{
		"manifest_version": "0.2",
		"name":             "dibs",
		"display_name":     "Dibs",
		"version":          version,
		"description": "Tells an AI agent when another is already doing its work. " +
			"Board, typed mail, directory claims. This bundle is macOS on Apple silicon " +
			"ONLY: Dibs ships no Intel Mac build. Linux uses the release archives; there is no " +
			"Windows build.",
		"long_description": longDescription,
		"author":           map[string]any{"name": "Agenxy", "url": "https://agenxy.org"},
		"homepage":         "https://agenxy.org/projects/dibs/",
		"repository":       map[string]any{"type": "git", "url": "https://github.com/Agenxy/dibs"},
		"license":          "Apache-2.0",
		"keywords":         []string{"agents", "coordination", "fleet", "mcp"},
		"server": map[string]any{
			"type":        "binary",
			"entry_point": "server/darwin-arm64/dibs",
			"mcp_config": map[string]any{
				"command": "${__dirname}/server/darwin-arm64/dibs",
				"args":    []string{"mcp-stdio"},
				"env":     map[string]string{},
			},
		},
		"compatibility": map[string]any{"platforms": []string{"darwin"}},
	}
	out, _ := json.MarshalIndent(m, "", "  ")
	return append(out, '\n')
}

const longDescription = "Dibs is a local coordination service for fleets of AI agents: a board of " +
	"who is working on what, typed mail between agents, and directory claims that " +
	"catch two agents about to edit the same files. The server in this bundle is " +
	"the stdio bridge (`dibs mcp-stdio`); it talks to the daemon, `dibd`, which is " +
	"beside it in the bundle and must be running: start it once with `dibd` (data in " +
	"~/.dibs, listening on 127.0.0.1:4777). `dibs doctor` says what is not working."

const readme = `# Dibs, as an MCP Bundle

Two binaries under server/darwin-arm64/: dibs (the MCP server, run as
` + "`dibs mcp-stdio`" + `, which is what the manifest launches) and dibd (the daemon it
talks to). The daemon is not started by the host: run ` + "`dibd`" + ` once, and
` + "`dibs doctor`" + ` afterwards says what is not working.

This bundle is macOS on Apple silicon only, and a manifest cannot say so: it
names operating systems, not architectures. Dibs ships no Intel Mac build
(an Intel Mac builds from source), Linux uses the release archives, and there
is no Windows build: Windows builds and vets in CI and nothing is published
for it.

Source, documentation and the release archives: https://github.com/Agenxy/dibs
`
