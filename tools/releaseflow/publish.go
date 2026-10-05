package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/agenxy/dibs/internal/selfupdate"
)

var checksumPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const immutableReleaseHint = "hint: the operator must enable repository Settings > General > Releases > " +
	"Enable release immutability for Agenxy/dibs; verify the public release reports immutable:true"

const (
	releasePageSize = 100
	releaseMaxPages = 10
)

type releaseStatus struct {
	ID        uint64                  `json:"id"`
	Tag       string                  `json:"tag_name"`
	Draft     bool                    `json:"draft"`
	Immutable bool                    `json:"immutable"`
	Assets    []struct{ Name string } `json:"assets"`
}

func status(ctx context.Context, c config, run runner) (releaseStatus, bool, error) {
	if c.negativeControl {
		return legacyDiscovery(ctx, c, run)
	}
	return listedStatus(ctx, c, run)
}

func listedStatus(ctx context.Context, c config, run runner) (releaseStatus, bool, error) {
	// Get-by-tag omits drafts even for a writer. Listing is the measured API
	// door that includes them. Scan every page, not just until the first match,
	// so a second exact tag cannot be silently accepted. An incomplete scan
	// (including a 404 for the repository itself) never proves absence.
	var found releaseStatus
	exists := false
	for page := 1; page <= releaseMaxPages; page++ {
		releases, err := listReleasePage(ctx, c, page, run)
		if err != nil {
			return releaseStatus{}, false, err
		}
		for _, s := range releases {
			if s.Tag != targetOf(c).tag {
				continue
			}
			if exists {
				return releaseStatus{}, false, fmt.Errorf("multiple releases match v%s; refuse ambiguous publication", c.version)
			}
			found, exists = s, true
		}
		if len(releases) < releasePageSize {
			return found, exists, nil
		}
	}
	return releaseStatus{}, false,
		fmt.Errorf("release discovery exceeded %d pages; refuse incomplete publication", releaseMaxPages)
}

func listReleasePage(ctx context.Context, c config, page int, run runner) ([]releaseStatus, error) {
	endpoint := fmt.Sprintf("repos/%s/releases?per_page=%d&page=%d", targetOf(c).repository, releasePageSize, page)
	out, err := run(ctx, nil, "gh", "api", endpoint)
	if err != nil {
		return nil, fmt.Errorf("list release page %d: %w", page, err)
	}
	var releases []releaseStatus
	if err = json.Unmarshal(out, &releases); err != nil {
		return nil, fmt.Errorf("decode release page %d: %w", page, err)
	}
	if releases == nil || len(releases) > releasePageSize {
		return nil, fmt.Errorf("release page %d is not a bounded array", page)
	}
	for _, s := range releases {
		if s.Tag == "" {
			return nil, fmt.Errorf("release page %d contains an entry without tag_name", page)
		}
	}
	return releases, nil
}

func assets(version string) []string {
	names := []string{selfupdate.ChecksumsName, selfupdate.BundleName, "dibs.mcpb", "dibs.mcpb.sha256", "dibs.rb"}
	for _, target := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		name, _ := selfupdate.ArchiveName(version, target[0], target[1])
		names = append(names, name, name+".sbom.json")
	}
	return names
}

func publish(ctx context.Context, c config, run runner) error {
	s, exists, err := status(ctx, c, run)
	if err != nil {
		return err
	}
	if exists && !s.Draft {
		// Never rebuild, re-sign, clobber, or un-publish public release bytes.
		if err = requireImmutablePublic(s); err != nil {
			return err
		}
		dir, err := download(ctx, c, s, run)
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		return validateTargetAssets(ctx, c, dir, run)
	}
	if !exists {
		if err = createDraft(ctx, c, run); err != nil {
			return err
		}
	}
	if err = buildSignedStage(ctx, c, run); err != nil {
		return err
	}
	return publishDraft(ctx, c, run)
}

func createDraft(ctx context.Context, c config, run runner) error {
	d := targetOf(c)
	body := "Install: `brew install agenxy/tap/dibs`.\n\n" +
		"Verify checksums.txt with checksums.txt.bundle using cosign 3, OIDC issuer " +
		"https://token.actions.githubusercontent.com and certificate identity https://github.com/" +
		repository + "/" + workflowPath + "@refs/tags/v" + c.version + ".\n\n" +
		"Full notes: https://github.com/" + repository + "/blob/v" + c.version + "/CHANGELOG.md\n"
	title := d.tag
	if d.rehearsal {
		title = rehearsalWarning + " (" + d.tag + ")"
		body = rehearsalWarning + ".\n\nLocal-only cask and registry plans; no downstream publication.\n" +
			"Exact rehearsal identity: " + d.identity() + "\n"
	}
	_, err := run(ctx, nil, "gh", "release", "create", d.tag, "--repo", d.repository,
		"--verify-tag", "--draft", "--title", title, "--generate-notes", "--notes", body)
	return err
}

func buildSignedStage(ctx context.Context, c config, run runner) error {
	// GoReleaser still owns build, macOS signing, archives, SBOMs and cask
	// generation. Publishing is deliberately split out so no public bytes exist
	// before the complete asset set and exact-tag signature are verified.
	env := []string{"HOMEBREW_TAP_DEPLOY_KEY=unused-offline-generation"}
	if targetOf(c).rehearsal {
		// Measured on the installed GoReleaser: this fixes the canonical build
		// version while GITHUB_REF (and therefore OIDC) stays on the unique ref.
		env = append(env, "GORELEASER_CURRENT_TAG=v"+c.version)
	}
	if _, err := run(ctx, env, "goreleaser", "release", "--clean", "--skip=sign,publish,announce"); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "go", "run", "./tools/archivecheck"); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "go", "run", "./tools/mcpbundle", "-version", c.version); err != nil {
		return err
	}
	if err := stageUnsigned(c); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "cosign", "sign-blob", "--yes", "--bundle",
		filepath.Join("dist", selfupdate.BundleName), filepath.Join("dist", selfupdate.ChecksumsName)); err != nil {
		return err
	}
	return validateTargetAssets(ctx, c, "dist", run)
}

var errDraftDisappeared = errors.New("draft disappeared or became public; " +
	"refuse asset mutation and retry read-only verification")

func publishDraft(ctx context.Context, c config, run runner) error {
	// Keep the draft check; the operator-owned immutable-release setting closes
	// the check/upload race against other authorised writers server-side.
	s, exists, err := status(ctx, c, run)
	if err != nil {
		return err
	}
	if !exists || !s.Draft {
		return errDraftDisappeared
	}
	if c.publicationAudit != nil {
		c.publicationAudit.draftDiscovered = true
	}
	if err = uploadDraft(ctx, c, run); err != nil {
		// The upload may have been refused because another writer published it,
		// or its response may have been lost. Never retry a mutation here: accept
		// only an immutable public release whose verified bytes equal our stage.
		return confirmPublished(ctx, c, run, err)
	}
	if c.publicationAudit != nil {
		c.publicationAudit.uploaded = true
	}
	s, exists, err = status(ctx, c, run)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("release disappeared before verification")
	}
	if !s.Draft {
		return verifyPublicStage(ctx, c, s, run)
	}
	if err = checkReadback(ctx, c, s, run); err != nil {
		return err
	}
	if c.publicationAudit != nil {
		c.publicationAudit.draftReadback = true
	}
	if err = remoteTag(ctx, c, true, run); err != nil {
		return err
	}
	d := targetOf(c)
	_, err = run(ctx, nil, "gh", "release", "edit", d.tag, "--repo", d.repository, "--draft=false")
	return confirmPublished(ctx, c, run, err)
}

func uploadDraft(ctx context.Context, c config, run runner) error {
	d := targetOf(c)
	args := []string{"release", "upload", d.tag, "--repo", d.repository, "--clobber"}
	for _, name := range assets(c.version) {
		args = append(args, filepath.Join("dist", name))
	}
	_, err := run(ctx, nil, "gh", args...)
	return err
}

func requireImmutablePublic(s releaseStatus) error {
	if s.Draft || !s.Immutable {
		return fmt.Errorf("release is not immutable and public; %s", immutableReleaseHint)
	}
	return nil
}

func confirmPublished(ctx context.Context, c config, run runner, cause error) error {
	s, exists, err := status(ctx, c, run)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("cannot confirm immutable public release: %w; %s", err, immutableReleaseHint))
	}
	if !exists {
		return errors.Join(cause, fmt.Errorf("public release is absent; %s", immutableReleaseHint))
	}
	if err = verifyPublicStage(ctx, c, s, run); err != nil {
		return errors.Join(cause, err)
	}
	return nil
}

func verifyPublicStage(ctx context.Context, c config, s releaseStatus, run runner) error {
	if err := requireImmutablePublic(s); err != nil {
		return err
	}
	// Re-read public bytes too: draft bytes could change between verification
	// and publication. Neither an ambiguous command nor immutable:true alone
	// proves these are the bytes we staged and signed.
	if err := checkReadback(ctx, c, s, run); err != nil {
		return err
	}
	return remoteTag(ctx, c, true, run)
}

func checkReadback(ctx context.Context, c config, s releaseStatus, run runner) error {
	dir, err := download(ctx, c, s, run)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err = validateTargetAssets(ctx, c, dir, run); err != nil {
		return err
	}
	for _, name := range assets(c.version) {
		staged, err := digest(filepath.Join("dist", name))
		if err != nil {
			return err
		}
		uploaded, err := digest(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if staged != uploaded {
			return fmt.Errorf("release asset %s differs from verified staging", name)
		}
	}
	return nil
}

func download(ctx context.Context, c config, s releaseStatus, run runner) (string, error) {
	seen := make(map[string]bool)
	for _, a := range s.Assets {
		if seen[a.Name] {
			return "", fmt.Errorf("release has duplicate asset %s", a.Name)
		}
		seen[a.Name] = true
	}
	for _, name := range assets(c.version) {
		if !seen[name] {
			return "", fmt.Errorf("release lacks required asset %s", name)
		}
	}
	dir, err := os.MkdirTemp("", "dibs-release-assets-")
	if err != nil {
		return "", err
	}
	d := targetOf(c)
	args := []string{"release", "download", d.tag, "--repo", d.repository, "--dir", dir}
	for _, name := range assets(c.version) {
		args = append(args, "--pattern", name)
	}
	if _, err = run(ctx, nil, "gh", args...); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func completeChecksums(c config, dir string) error {
	path := filepath.Join(dir, selfupdate.ChecksumsName)
	// #nosec G304,G703 -- fixed checksum filename in the runner-owned staging directory.
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(string(data)) + "\n"
	// A malformed or duplicate producer entry is not a missing entry. Preserve
	// the producer's archive-member receipts, and refuse ambiguity before signing.
	seen := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		sum, name, found := strings.Cut(line, "  ")
		if !found || !checksumPattern.MatchString(sum) || name == "" || strings.TrimSpace(name) != name {
			return errors.New("malformed generated checksum entry")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate generated checksum entry %s", name)
		}
		seen[name] = sum
	}
	for _, name := range assets(c.version) {
		if name == selfupdate.ChecksumsName || name == selfupdate.BundleName {
			continue
		}
		sum, err := digest(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		old, exists := seen[name]
		if exists {
			if sum != old {
				return fmt.Errorf("generated asset %s disagrees with original checksums", name)
			}
			continue
		}
		text += sum + "  " + name + "\n"
	}
	// #nosec G703 -- same runner-owned staging file, never a dispatch input.
	return os.WriteFile(path, []byte(text), 0o600)
}

func stageUnsigned(c config) error {
	cask, err := os.ReadFile(filepath.Join("dist", "homebrew", "Casks", "dibs.rb"))
	if err != nil {
		return err
	}
	cask, err = stageCask(c, cask)
	if err != nil {
		return err
	}
	// #nosec G703 -- fixed output in the GoReleaser stage, never a dispatch input.
	if err = os.WriteFile(filepath.Join("dist", "dibs.rb"), cask, 0o600); err != nil {
		return err
	}
	return completeChecksums(c, "dist")
}

func validateAssets(ctx context.Context, c config, dir string) error {
	// #nosec G304 -- fixed public evidence names in the owned stage/download directory.
	checksums, err := os.ReadFile(filepath.Join(dir, selfupdate.ChecksumsName))
	if err != nil {
		return err
	}
	// #nosec G304 -- same bounded verification evidence door as checksums above.
	bundle, err := os.ReadFile(filepath.Join(dir, selfupdate.BundleName))
	if err != nil {
		return err
	}
	proof, err := selfupdate.VerifyReleaseEvidence(ctx, "v"+c.version, checksums, bundle)
	if err != nil {
		return err
	}
	for _, name := range assets(c.version) {
		if name == selfupdate.ChecksumsName || name == selfupdate.BundleName {
			continue
		}
		want, err := selfupdate.ChecksumFor(proof.Checksums(), name)
		if err != nil {
			return err
		}
		got, err := digest(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("release asset %s does not match exact-tag signed checksums", name)
		}
	}
	return nil
}

func digest(path string) (string, error) {
	// #nosec G703 -- fixed asset basename under this call's owned stage/download directory.
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("release asset is not a regular file")
	}
	// #nosec G304,G703 -- fixed asset names in an owned stage, checked above.
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
