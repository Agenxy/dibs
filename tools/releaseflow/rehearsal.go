package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/agenxy/dibs/internal/selfupdate"
)

type publicationAudit struct {
	draftDiscovered, uploaded, draftReadback bool
}

// This is deliberately the OLD discovery door, not an old publisher with a
// production repository baked into it. Only the closed scratch target may use
// it. The negative control therefore changes discovery and nothing else.
func legacyDiscovery(ctx context.Context, c config, run runner) (releaseStatus, bool, error) {
	d := targetOf(c)
	if !d.rehearsal {
		return releaseStatus{}, false, errors.New("legacy discovery is NEVER a production control")
	}
	out, err := run(ctx, nil, "gh", "api", "repos/"+d.repository+"/releases/tags/"+d.tag)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404") {
			return releaseStatus{}, false, nil
		}
		return releaseStatus{}, false, err
	}
	var s releaseStatus
	if err = json.Unmarshal(out, &s); err != nil || s.Tag != d.tag {
		return releaseStatus{}, false, errors.New("legacy lookup returned invalid or different tag")
	}
	return s, true, nil
}

// Separate verifier: the installed self-update policy remains exact production
// identity only. A rehearsal signature is not a VerifiedRelease and can NEVER
// become installation evidence through this door.
func validateTargetAssets(ctx context.Context, c config, dir string, run runner) error {
	d := targetOf(c)
	if !d.rehearsal {
		return validateAssets(ctx, c, dir)
	}
	if err := verifyScratchSignature(ctx, c,
		filepath.Join(dir, selfupdate.ChecksumsName), filepath.Join(dir, selfupdate.BundleName), run); err != nil {
		return err
	}
	// #nosec G304 -- fixed name in the owned stage/download directory.
	checksums, err := os.ReadFile(filepath.Join(dir, selfupdate.ChecksumsName))
	if err != nil {
		return err
	}
	for _, name := range assets(c.version) {
		if name == selfupdate.ChecksumsName || name == selfupdate.BundleName {
			continue
		}
		want, err := selfupdate.ChecksumFor(string(checksums), name)
		if err != nil {
			return err
		}
		got, err := digest(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("rehearsal asset %s differs from scratch-signed checksums", name)
		}
	}
	return nil
}

func verifyScratchSignature(ctx context.Context, c config, blob, bundle string, run runner) error {
	d := targetOf(c)
	if !d.rehearsal {
		return errors.New("scratch signature verifier refuses production target")
	}
	root, err := selfupdate.PinnedSigstoreRoot()
	if err != nil {
		return err
	}
	cache, err := os.MkdirTemp("", "dibs-rehearsal-tuf-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(cache) }()
	rootPath := filepath.Join(cache, "trusted-root.json")
	if err = os.WriteFile(rootPath, root, 0o600); err != nil {
		return err
	}
	env := []string{"TUF_MIRROR=", "TUF_ROOT_JSON=", "TUF_ROOT=" + filepath.Join(cache, "unused-cache")}
	_, err = run(ctx, env, "cosign", "verify-blob", blob,
		"--bundle", bundle,
		"--trusted-root", rootPath,
		"--certificate-identity", d.identity(),
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com")
	if err != nil {
		return fmt.Errorf("scratch-namespace signature verification: %w", err)
	}
	return nil
}

func fullPublication(ctx context.Context, c config, run runner) error {
	objects, err := validateRehearsal(ctx, c, run)
	if err != nil {
		return err
	}
	if c.phase == "full-publication-validate" {
		return nil
	}
	if err = prepareRehearsal(ctx, c, run); err != nil {
		return err
	}
	c.publicationAudit = new(publicationAudit)
	err = publish(ctx, c, run)
	if c.negativeControl {
		return negativePublication(ctx, c, run, err)
	}
	if err != nil {
		return err
	}
	return finishRehearsal(ctx, c, objects, run)
}

func validateRehearsal(ctx context.Context, c config, run runner) (map[string]string, error) {
	if err := rehearsalContext(c); err != nil {
		return nil, err
	}
	objects, err := sourceObjects(ctx, c, run)
	if err != nil {
		return nil, err
	}
	if err = checkoutMatches(ctx, c.sha, objects["tree"], run); err != nil {
		return nil, err
	}
	if err = cleanRehearsalCheckout(ctx, run); err != nil {
		return nil, err
	}
	if err = remoteTag(ctx, c, true, run); err != nil {
		return nil, err
	}
	return objects, nil
}

func prepareRehearsal(ctx context.Context, c config, run runner) error {
	// LOCAL canonical tag only: release OIDC still refers to the unique scratch
	// ref. Never push it, and never execute this phase in the production repo.
	if err := localTag(ctx, c, run); err != nil {
		return err
	}
	if _, err := run(ctx, nil, "go", "run", "./tools/sigstore-root-check"); err != nil {
		return err
	}
	// A public retry verifies bytes, but cannot prove the upload/draft doors.
	s, exists, err := listedStatus(ctx, c, run)
	if err != nil {
		return err
	}
	if exists && (!s.Draft || len(s.Assets) != 0) {
		return errors.New("full-publication needs absent or empty draft target; " +
			"use a fresh candidate, never mutate public bytes")
	}
	if c.negativeControl && exists {
		return errors.New("discovery negative control requires an absent target; " +
			"an existing draft would fail creation instead of discovery")
	}
	return nil
}

func negativePublication(ctx context.Context, c config, run runner, cause error) error {
	if !errors.Is(cause, errDraftDisappeared) {
		return fmt.Errorf("discovery negative control failed for different reason: %w", cause)
	}
	actual, found, err := listedStatus(ctx, c, run)
	if err != nil {
		return err
	}
	if !found || !actual.Draft || len(actual.Assets) != 0 {
		return errors.New("negative control did not leave exact empty draft visible through LIST")
	}
	return fmt.Errorf("CONFIRMED discovery-only negative control: LIST sees exact empty draft %d "+
		"while old get-by-tag misses it: %w", actual.ID, cause)
}

func finishRehearsal(ctx context.Context, c config, objects map[string]string, run runner) error {
	audit := c.publicationAudit
	if !audit.draftDiscovered || !audit.uploaded || !audit.draftReadback {
		return errors.New("publisher missed a draft discovery/upload/readback door; no full-publication receipt")
	}
	if err := publish(ctx, c, readOnlyPublication(run)); err != nil {
		return fmt.Errorf("public retry is not read-only verified: %w", err)
	}
	s, exists, err := listedStatus(ctx, c, run)
	if err != nil || !exists {
		return errors.Join(err, errors.New("rehearsal public release missing at proof time"))
	}
	if err = requireImmutablePublic(s); err != nil || s.ID == 0 {
		return errors.Join(err, errors.New("rehearsal requires exact positive immutable release ID"))
	}
	dir, err := download(ctx, c, s, run)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err = validateTargetAssets(ctx, c, dir, run); err != nil {
		return err
	}
	sums, err := assetDigests(c, dir)
	if err != nil {
		return err
	}
	if err = dryDownstreamPlans(c, dir); err != nil {
		return err
	}
	if err = cleanRehearsalCheckout(ctx, run); err != nil {
		return err
	}
	if err = writePublication(c, objects, s.ID, sums); err != nil {
		return err
	}
	return publishEvidence(ctx, c, run)
}

func cleanRehearsalCheckout(ctx context.Context, run runner) error {
	out, err := run(ctx, nil, "git", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) != "" {
		return errors.New("full-publication candidate contains tracked modifications")
	}
	return nil
}

func readOnlyPublication(run runner) runner {
	return func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		// Explicit allow-list, not a mutation blacklist: unknown operations fail.
		if name == "gh" && len(args) >= 2 && (args[0] == "api" && len(args) == 2 ||
			args[0] == "release" && args[1] == "download") ||
			name == "cosign" && len(args) > 0 && args[0] == "verify-blob" {
			return run(ctx, env, name, args...)
		}
		return nil, fmt.Errorf("public retry attempted a non-read-only operation: %s %v", name, args)
	}
}

func assetDigests(c config, dir string) (map[string]string, error) {
	sums := make(map[string]string)
	for _, name := range assets(c.version) {
		sum, err := digest(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		sums[name] = sum
	}
	return sums, nil
}

// These are validated DRY plans. They do not claim Homebrew merge/install or
// registry acceptance and never invoke either publisher or their credentials.
var caskAssetPattern = regexp.MustCompile(`sha256 "([0-9a-f]{64})"\s+url "([^"]+)"`)

func validateDryCask(c config, dir string) error {
	// #nosec G304 -- fixed cask name in verified release bytes.
	cask, err := os.ReadFile(filepath.Join(dir, "dibs.rb"))
	if err != nil {
		return err
	}
	return validateCaskBytes(c, dir, cask, targetOf(c))
}

func validateCaskBytes(c config, dir string, cask []byte, d publicationTarget) error {
	if !strings.Contains(string(cask), `version "`+c.version+`"`) {
		return errors.New("dry cask plan has a different canonical version")
	}
	expanded := strings.ReplaceAll(string(cask), "#{version}", c.version)
	entries := caskAssetPattern.FindAllStringSubmatch(expanded, -1)
	if len(entries) != 3 {
		return errors.New("dry cask plan must have exactly three archive URL/checksum pairs")
	}
	caskSums := make(map[string]string)
	for _, entry := range entries {
		if _, exists := caskSums[entry[2]]; exists {
			return errors.New("dry cask plan has duplicate archive URLs")
		}
		caskSums[entry[2]] = entry[1]
	}
	for _, pair := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		name, _ := selfupdate.ArchiveName(c.version, pair[0], pair[1])
		sum, err := digest(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		url := "https://github.com/" + d.repository + "/releases/download/" + d.tag + "/" + name
		if caskSums[url] != sum {
			return fmt.Errorf("dry cask plan lacks verified archive URL/checksum for %s", name)
		}
	}
	return nil
}

// GoReleaser uses the scratch repository but the canonical build tag in its
// generated cask. That tag is LOCAL ONLY and has no scratch release. Before
// signing, prove the three generated pairs and bind this dry plan to the real
// unique-tag payload. Production cask bytes are never rewritten here.
func stageCask(c config, cask []byte) ([]byte, error) {
	d := targetOf(c)
	if !d.rehearsal {
		return cask, nil
	}
	generated := d
	generated.tag = "v" + c.version
	if err := validateCaskBytes(c, "dist", cask, generated); err != nil {
		return nil, fmt.Errorf("generated scratch cask: %w", err)
	}
	expanded := strings.ReplaceAll(string(cask), "#{version}", c.version)
	for _, pair := range [][2]string{{"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		name, _ := selfupdate.ArchiveName(c.version, pair[0], pair[1])
		prefix := "https://github.com/" + d.repository + "/releases/download/"
		from := `url "` + prefix + generated.tag + "/" + name + `"`
		to := `url "` + prefix + d.tag + "/" + name + `"`
		expanded = strings.ReplaceAll(expanded, from, to)
	}
	staged := []byte(expanded)
	if err := validateCaskBytes(c, "dist", staged, d); err != nil {
		return nil, err
	}
	return staged, nil
}

func dryDownstreamPlans(c config, dir string) error {
	if err := validateDryCask(c, dir); err != nil {
		return err
	}
	data, err := os.ReadFile("server.json")
	if err != nil {
		return err
	}
	var doc map[string]any
	if err = json.Unmarshal(data, &doc); err != nil {
		return err
	}
	if doc["version"] != c.version || doc["name"] != "io.github.Agenxy/dibs" {
		return errors.New("dry registry plan requires canonical candidate server identity/version")
	}
	sum, err := digest(filepath.Join(dir, "dibs.mcpb"))
	if err != nil {
		return err
	}
	// #nosec G304 -- fixed sidecar in the verified public download.
	sidecar, err := os.ReadFile(filepath.Join(dir, "dibs.mcpb.sha256"))
	if err != nil {
		return err
	}
	if fields := strings.Fields(string(sidecar)); len(fields) != 2 || fields[0] != sum || fields[1] != "dibs.mcpb" {
		return errors.New("dry registry plan's bundle sidecar disagrees with the actual public bundle")
	}
	d := targetOf(c)
	// Generate the exact would-be package entry, but KEEP it in the dry plan,
	// not server.json and not a production registry request.
	doc["packages"] = []any{map[string]any{
		"registryType": "mcpb",
		"identifier":   "https://github.com/" + d.repository + "/releases/download/" + d.tag + "/dibs.mcpb",
		"version":      c.version, "fileSha256": sum, "transport": map[string]any{"type": "stdio"},
	}}
	plan := map[string]any{
		"scope": "dry-plan-only", "not_live_acceptance": true,
		"version": c.version, "registry_manifest": doc,
		"bundle_sha256":      sum,
		"bundle_url":         "https://github.com/" + d.repository + "/releases/download/" + d.tag + "/dibs.mcpb",
		"cask_review_branch": "cask-" + c.version, "warning": rehearsalWarning,
	}
	out, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile("dist/rehearsal-dry-plans.json", append(out, '\n'), 0o600)
}
