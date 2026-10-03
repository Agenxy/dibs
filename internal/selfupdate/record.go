package selfupdate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/agenxy/dibs/internal/release"
)

const (
	releaseRecordName = "guest-release.json"
	maxChecksums      = 64 << 10
	maxBundle         = 256 << 10
	maxReleaseRecord  = 512 << 10
)

// VerifiedRelease is immutable evidence, not a capability claim. Only online
// acquisition or offline signature verification can construct it. Saving the
// evidence never turns an editable JSON flag or root into authority.
type VerifiedRelease struct {
	tag, checksums, bundle string
}

// Tag is the exact workflow tag whose signature was verified.
func (v VerifiedRelease) Tag() string { return v.tag }

// Checksums returns the exact bytes verified, never a second network copy.
func (v VerifiedRelease) Checksums() string { return v.checksums }

// Byte slices marshal as base64, preserving the exact bundle byte string. This
// is a derived local cache, NOT a new signed release artifact or trust store.
type releaseRecord struct {
	Tag       string `json:"tag"`
	Checksums []byte `json:"checksums"`
	Bundle    []byte `json:"bundle"`
}

// ReleaseForTag validates a canonical stable tag before it enters a URL or a
// certificate identity. Ordering remains internal/release's one rule.
func ReleaseForTag(tag string) (Release, error) {
	v := strings.TrimPrefix(tag, "v")
	parts := strings.Split(v, ".")
	valid := strings.HasPrefix(tag, "v") && len(parts) == 3 && release.Comparable(v)
	for _, part := range parts {
		if part == "" {
			valid = false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				valid = false
			}
		}
	}
	if !valid {
		return Release{}, errors.New("release needs an exact stable tag, e.g. v1.2.3; no aliases or prereleases")
	}
	return Release{Version: v, Tag: tag, URL: "https://github.com/" + Repo + "/releases/tag/" + tag}, nil
}

// AcquireVerifiedRelease uses normal authenticated production TUF, explicitly
// online. The caller owns a private staging directory and chooses when to save
// the evidence. --allow-unsigned never enters this path.
func AcquireVerifiedRelease(ctx context.Context, c *http.Client, rel Release, dir string) (VerifiedRelease, error) {
	canonical, err := ReleaseForTag(rel.Tag)
	if err != nil || canonical.Version != rel.Version {
		return VerifiedRelease{}, errors.New("release tag and version are not the same canonical release")
	}
	checksums, err := Verify(ctx, c, rel, dir)
	if err != nil {
		return VerifiedRelease{}, err
	}
	// #nosec G304 -- fixed file written by Verify in the caller's private stage.
	f, err := os.Open(filepath.Join(dir, BundleName))
	if err != nil {
		return VerifiedRelease{}, err
	}
	defer func() { _ = f.Close() }()
	b, err := readBounded(f, maxBundle)
	if err != nil {
		return VerifiedRelease{}, err
	}
	return VerifiedRelease{tag: rel.Tag, checksums: checksums, bundle: string(b)}, nil
}

// Save retains public evidence before an upgrade's staging cleanup. It does
// NOT require this OLD binary's embedded root to understand a newer release:
// acquisition verified against authenticated current TUF, and the NEW binary
// rechecks against its own reviewed embedded root when reading the record.
func (v VerifiedRelease) Save(dir string) error {
	r := releaseRecord{Tag: v.tag, Checksums: []byte(v.checksums), Bundle: []byte(v.bundle)}
	if err := r.validate(); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir) // existing data directory only; no board initialization
	if err != nil {
		return fmt.Errorf("open existing release-record directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	if info, err := root.Lstat(releaseRecordName); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("release record is not a regular file; preserve it and repair the cache explicitly")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := ".guest-release-" + rand.Text()
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = root.Remove(tmp) }()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = root.Rename(tmp, releaseRecordName); err != nil {
		return err
	}
	// After rename, never delete on an ambiguous durability failure. The new
	// cache may already be visible; every later read still verifies its signature.
	d, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("record published but directory durability unconfirmed: %w", err)
	}
	defer func() { _ = d.Close() }()
	if err = d.Sync(); err != nil {
		return fmt.Errorf("record published but directory durability unconfirmed: %w", err)
	}
	return nil
}

// LoadVerifiedRelease snapshots the bounded candidate once and verifies its
// exact bytes against the root embedded in THIS binary. It has no HTTP client,
// initialize call or admission-time TUF refresh. Missing/corrupt evidence is a
// refusal, not permission to fetch replacement metadata or emit a guest recipe.
func LoadVerifiedRelease(ctx context.Context, dir, buildVersion string) (VerifiedRelease, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return VerifiedRelease{}, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(releaseRecordName)
	if err != nil {
		return VerifiedRelease{}, err
	}
	if !info.Mode().IsRegular() {
		return VerifiedRelease{}, errors.New("release record is not a regular file; retain signed evidence in the board's cache")
	}
	f, err := root.Open(releaseRecordName)
	if err != nil {
		return VerifiedRelease{}, err
	}
	defer func() { _ = f.Close() }()
	b, err := readBounded(f, maxReleaseRecord)
	if err != nil {
		return VerifiedRelease{}, err
	}
	var r releaseRecord
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&r); err != nil {
		return VerifiedRelease{}, fmt.Errorf("invalid retained release record: %w", err)
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return VerifiedRelease{}, errors.New("retained release record has trailing data")
	}
	if err = r.validate(); err != nil {
		return VerifiedRelease{}, err
	}
	v := VerifiedRelease{tag: r.Tag, checksums: string(r.Checksums), bundle: string(r.Bundle)}
	if err = v.verifyOffline(ctx, buildVersion); err != nil {
		return VerifiedRelease{}, err
	}
	return v, nil
}

func (r releaseRecord) validate() error {
	if _, err := ReleaseForTag(r.Tag); err != nil {
		return err
	}
	if len(r.Checksums) == 0 || len(r.Checksums) > maxChecksums || len(r.Bundle) == 0 || len(r.Bundle) > maxBundle {
		return errors.New("retained release evidence is missing or exceeds its bounded checksum/bundle size; verify the release explicitly")
	}
	return nil
}

func (v VerifiedRelease) verifyOffline(ctx context.Context, buildVersion string) error {
	cosign, err := usableCosign(ctx)
	if err != nil {
		return err
	}
	if err = checkTrustedRoot([]byte(sigstoreTrustedRoot)); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "dibs-release-proof-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for name, content := range map[string]string{
		ChecksumsName: v.checksums, BundleName: v.bundle, "trusted-root.json": sigstoreTrustedRoot,
	} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	// #nosec G204 -- usable executable, fixed args, validated tag, private snapshots.
	cmd := exec.CommandContext(ctx, cosign, "verify-blob", filepath.Join(dir, ChecksumsName),
		"--bundle", filepath.Join(dir, BundleName), "--trusted-root", filepath.Join(dir, "trusted-root.json"),
		"--certificate-identity", releaseIdentity(v.tag),
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com")
	cmd.Env = productionTUFEnv(os.Environ(), filepath.Join(dir, "unused-tuf-cache"))
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		hint := "treat the retained record as untrusted; no network fallback was attempted"
		if release.Comparable(buildVersion) {
			if newer, _ := release.Newer(v.tag, buildVersion); newer {
				hint += "; release " + v.tag + " is newer than this dibs build (" + buildVersion +
					"), so if Sigstore rotated keys since, upgrade dibs and verify again"
			}
		}
		return fmt.Errorf("retained release signature verification failed for %s: %w: %s; %s",
			v.tag, runErr, strings.TrimSpace(string(out)), hint)
	}
	return nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("release evidence exceeds %d bytes", limit)
	}
	return b, err
}

func releaseIdentity(tag string) string {
	return "https://github.com/" + Repo + "/.github/workflows/release.yml@refs/tags/" + tag
}
