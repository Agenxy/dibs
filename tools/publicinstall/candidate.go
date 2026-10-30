// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/agenxy/dibs/internal/release"
	"github.com/agenxy/dibs/internal/selfupdate"
)

var candidateSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (r *runner) nextCandidateTag() (string, error) {
	out, err := r.command("candidate-latest-tags", os.Environ(), "git", "tag", "--list", "v*")
	if err != nil {
		return "", err
	}
	latest := "v0.0.0"
	for tag := range strings.FieldsSeq(out) {
		if _, err := selfupdate.ReleaseForTag(tag); err != nil {
			continue
		}
		if newer, err := release.Newer(tag, latest); err == nil && newer {
			latest = tag
		}
	}
	parts := strings.Split(latest, ".")
	patch, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil || patch == 1<<32-1 {
		return "", errors.New("latest release tag cannot supply a next candidate patch")
	}
	return parts[0] + "." + parts[1] + "." + strconv.FormatUint(patch+1, 10), nil
}

type candidateFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// This is a transport manifest, not a signed release or publication receipt.
// The caller binds it to its exact Actions input SHA and canonical version.
type candidateManifest struct {
	Schema  int             `json:"schema"`
	SHA     string          `json:"sha"`
	Version string          `json:"version"`
	Files   []candidateFile `json:"files"`
}

func validateCandidateMode(o options, goos string) error {
	if !candidateSHA.MatchString(o.sha) {
		return errors.New("candidate acceptance requires a full lowercase SHA")
	}
	if o.mode == "candidate-build" {
		if goos != "darwin" {
			return errors.New("candidate artifacts must use the macOS release runner and production snapshot invocation")
		}
		return nil
	}
	if goos != "linux" || o.candidateDir == "" || o.fresh != 1 ||
		os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" {
		return errors.New("candidate-linux requires artifact input and one fresh ephemeral hosted Linux installation")
	}
	return nil
}

func (r *runner) buildCandidate(o options) error {
	repo, err := os.Getwd()
	if err != nil {
		return err
	}
	r.cwd = repo
	if o.tag == "" {
		o.tag, err = r.nextCandidateTag()
		if err != nil {
			return err
		}
		r.report.Tag = o.tag
	}
	head, err := r.command("candidate-head", os.Environ(), "git", "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != o.sha {
		return errors.New("candidate checkout does not match the exact requested SHA")
	}
	dirty, err := r.command("candidate-clean", os.Environ(), "git", "status", "--porcelain", "--untracked-files=normal")
	if err != nil || strings.TrimSpace(dirty) != "" {
		return errors.New("candidate source must be committed and clean before artifact generation")
	}
	if err := r.candidateTag(o); err != nil {
		return err
	}
	env := append(os.Environ(), "DIBS_SNAPSHOT_RELEASE_VERSION="+strings.TrimPrefix(o.tag, "v"),
		"HOMEBREW_TAP_DEPLOY_KEY=", "DIBS_SIGNING_P12=", "DIBS_SIGNING_P12_PASSWORD=",
		"DIBS_CODESIGN_IDENTITY=", "DIBS_SIGNING_KEYCHAIN=")
	if _, err := r.command("candidate-snapshot", env, "goreleaser", "release", "--snapshot", "--clean",
		"--skip=sign,publish,announce,validate"); err != nil {
		return err
	}
	manifest := candidateManifest{Schema: 1, SHA: o.sha, Version: strings.TrimPrefix(o.tag, "v")}
	for _, name := range candidateNames(manifest.Version) {
		source := filepath.Join(repo, "dist", name)
		if name == "dibs.rb" {
			source = filepath.Join(repo, "dist", "homebrew", "Casks", name)
		}
		if err := copyCandidateFile(source, filepath.Join(r.root, name)); err != nil {
			return err
		}
		digest, err := candidateDigest(filepath.Join(r.root, name))
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, candidateFile{name, digest})
	}
	return r.writeCandidateManifest(o, manifest)
}

func (r *runner) writeCandidateManifest(o options, manifest candidateManifest) error {
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.root, "candidate.json"), append(body, '\n'), 0o600); err != nil {
		return err
	}
	r.report.CandidateSHA = o.sha
	r.report.CandidateProof = "unsigned release-shaped snapshot; no publication or signature proof"
	if output := os.Getenv("GITHUB_OUTPUT"); output != "" {
		f, err := os.OpenFile(output, os.O_APPEND|os.O_WRONLY, 0) // #nosec G304 G703 -- Actions-owned output channel
		if err != nil {
			return err
		}
		_, writeErr := fmt.Fprintln(f, "tag="+o.tag)
		return errors.Join(writeErr, f.Close())
	}
	return nil
}

func (r *runner) candidateTag(o options) error {
	ref, err := r.command("candidate-tag-present", os.Environ(), "git", "tag", "--list", o.tag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(ref) != "" {
		head, err := r.command("candidate-tag-sha", os.Environ(), "git", "rev-parse", o.tag+"^{commit}")
		if err != nil || strings.TrimSpace(head) != o.sha {
			return errors.New("existing candidate tag names different source; never replace it")
		}
		return nil
	}
	_, err = r.command("candidate-local-tag", os.Environ(), "git", "-c", "tag.gpgsign=false",
		"-c", "user.name=Dibs candidate acceptance", "-c", "user.email=acceptance@users.noreply.github.com",
		"tag", "-a", o.tag, o.sha, "-m", o.tag)
	return err
}

func candidateNames(version string) []string {
	return []string{
		"dibs.rb", "checksums.txt", "dibs_" + version + "_darwin_arm64.tar.gz",
		"dibs_" + version + "_linux_amd64.tar.gz", "dibs_" + version + "_linux_arm64.tar.gz",
	}
}

func candidateDigest(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- explicit candidate input or fixed name under the new output root
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("candidate artifact must be a regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyCandidateFile(from, to string) error {
	src, err := os.Open(from) // #nosec G304 -- explicit snapshot/archive/fixture source, never executable selection
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	// #nosec G304 -- new invocation-owned destination
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dst, src)
	return errors.Join(copyErr, dst.Close())
}

func readCandidate(o options) (candidateManifest, error) {
	var m candidateManifest
	body, err := os.ReadFile(filepath.Join(o.candidateDir, "candidate.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return m, err
	}
	if m.Schema != 1 || m.SHA != o.sha || m.Version != strings.TrimPrefix(o.tag, "v") ||
		len(m.Files) != len(candidateNames(m.Version)) {
		return m, errors.New("candidate manifest does not bind the exact input SHA/version/artifact set")
	}
	for i, name := range candidateNames(m.Version) {
		if m.Files[i].Name != name {
			return m, errors.New("candidate manifest has an unexpected or reordered path")
		}
		digest, err := candidateDigest(filepath.Join(o.candidateDir, name))
		if err != nil || digest != m.Files[i].SHA256 {
			return m, fmt.Errorf("candidate artifact digest mismatch: %s", name)
		}
	}
	return m, nil
}

func adaptCandidateCask(original []byte, tag, origin string) ([]byte, error) {
	versions := regexp.MustCompile(`(?m)^\s+version "([^"]+)"$`).FindAllSubmatch(original, -1)
	if len(versions) != 1 || string(versions[0][1]) != strings.TrimPrefix(tag, "v") {
		return nil, errors.New("generated cask version differs from the exact candidate version")
	}
	prefix, err := candidateURLPrefix(original, tag)
	if err != nil {
		return nil, err
	}
	destination := origin + strings.TrimPrefix(prefix, "https://github.com")
	adapted := strings.ReplaceAll(string(original), `url "`+prefix, `url "`+destination)
	if strings.ReplaceAll(adapted, `url "`+destination, `url "`+prefix) != string(original) {
		return nil, errors.New("candidate cask changed beyond its download origin")
	}
	return []byte(adapted), nil
}

func candidateURLPrefix(original []byte, tag string) (string, error) {
	// GoReleaser uses #{version} for the tag when it equals the archive
	// version. Snapshot suffixes instead require a literal release tag.
	for _, value := range []string{tag, "v#{version}"} {
		prefix := "https://github.com/Agenxy/dibs/releases/download/" + value
		urls := regexp.MustCompile(`(?m)^\s+url "` + regexp.QuoteMeta(prefix) + `/dibs_#\{version\}_`)
		if len(urls.FindAll(original, -1)) == 3 && strings.Count(string(original), prefix) == 3 {
			return prefix, nil
		}
	}
	return "", errors.New("generated cask does not have exactly the three expected release archive URLs")
}

func (r *runner) installCandidate(o options) error {
	m, err := readCandidate(o)
	if err != nil {
		return err
	}
	r.report.CandidateSHA, r.report.CandidateProof = m.SHA,
		"exact Actions candidate artifact digests; unsigned, no published signature claim"
	r.report.Environment = "GitHub-hosted Linux runner"
	r.env = isolatedEnv(os.Getenv("HOME"), r.root)
	server := httptest.NewServer(http.StripPrefix("/Agenxy/dibs/releases/download/"+o.tag,
		http.FileServer(http.Dir(o.candidateDir))))
	defer server.Close()
	name := "dibs_" + m.Version + "_linux_" + runtime.GOARCH + ".tar.gz"
	if err := r.candidateBrew(o, server.URL); err != nil {
		return err
	}
	archive := filepath.Join(r.root, "candidate-archive")
	if err := os.Mkdir(archive, 0o700); err != nil {
		return err
	}
	_, err = r.command("candidate-archive-extract", r.env, "tar", "-xzf",
		filepath.Join(o.candidateDir, name), "-C", archive)
	if err != nil {
		return err
	}
	return r.candidateServiceProof(archive)
}

// Keep the same public release signature verifier for the old-code control.
func (r *runner) oldPublishedBinaries() (string, error) {
	dir := filepath.Join(r.root, "published-v0.0.16-control")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	rel, err := selfupdate.ReleaseForTag("v0.0.16")
	if err != nil {
		return "", err
	}
	client := &http.Client{}
	checksums, err := selfupdate.Verify(r.ctx, client, rel, dir)
	if err != nil {
		return "", err
	}
	return dir, selfupdate.Fetch(r.ctx, client, rel, "linux", runtime.GOARCH, dir, checksums)
}
