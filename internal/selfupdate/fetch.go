package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// maxAsset bounds what will be pulled into memory or onto disk from a release.
// The largest archive Dibs publishes is under 10MB; this is room for that to
// grow a great deal and a refusal long before a mistake or a hostile answer
// fills the disk.
const maxAsset = 256 << 20

// Payload is what an installed Dibs consists of, as the archive carries it and
// as `task install` places it.
//
// The documentation files and the man pages in the archive are deliberately
// not here: an install puts the runtime beside the binaries and leaves the
// prose to the package manager. The Swift helper and the notifier bundle ARE
// here, because the runtime looks for both BESIDE the executable and the
// release exists to carry them: shipping the two binaries alone is exactly the
// hole that put Touch ID and notifications in the documentation and in no
// published archive for several versions.
var payload = []string{"dibs", "dibd", "dibs-presence", "Dibs.app"}

// Payload is what an install consists of.
func Payload() []string { return append([]string(nil), payload...) }

// Fetch downloads one release, proves it is the one the project published,
// and lays its payload out in dest.
//
// Nothing is installed here. The caller decides where these files go and when,
// because putting them in place is the step that has to be ordered against
// stopping a daemon, and that order is `dibs upgrade`'s to keep.
// checksums is the file Verify proved, handed back so that the digest checked
// here is read from the bytes whose signature was checked. Empty means
// nothing was verified (--allow-unsigned) and the file is fetched here.
func Fetch(ctx context.Context, c *http.Client, rel Release, goos, goarch, dest, checksums string) error {
	return fetchFrom(ctx, c, rel, goos, goarch, dest, releaseBase, checksums)
}

// fetchFrom is Fetch against a stated origin, so the digest comparison and
// the unpacking can be tested against a server serving a real archive. The
// origin is not a caller's to choose: an update source is a trust decision,
// and one that could be pointed elsewhere is a way to install somebody else's
// binary as this machine's daemon.
func fetchFrom(
	ctx context.Context, c *http.Client, rel Release, goos, goarch, dest, base, checksums string,
) error {
	asset, err := ArchiveName(rel.Version, goos, goarch)
	if err != nil {
		return err
	}
	// THE BYTES VERIFY PROVED, not a second copy of them.
	//
	// The first version of this had Verify download checksums.txt, check the
	// signature over it, and then had Fetch download checksums.txt AGAIN and
	// read the digest out of that. Two fetches, so the file whose signature
	// was checked and the file the digest came from were not provably the
	// same bytes, and a server that answered differently the second time
	// would have been believed. The whole chain hangs on this one file, so it
	// is read once and passed along.
	if checksums == "" {
		if checksums, err = getText(ctx, c, assetURL(base, rel.Tag, ChecksumsName)); err != nil {
			return fmt.Errorf("fetching %s for %s: %w", ChecksumsName, rel.Tag, err)
		}
	}
	want, err := ChecksumFor(checksums, asset)
	if err != nil {
		return err
	}
	archive := filepath.Join(dest, asset)
	sum, err := download(ctx, c, assetURL(base, rel.Tag, asset), archive)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", asset, err)
	}
	if sum != want {
		return fmt.Errorf("%s does not match the digest %s publishes for it "+
			"(got %s, expected %s): the download is not what was released, and nothing "+
			"has been installed", asset, ChecksumsName, sum, want)
	}
	return extract(archive, dest)
}

// Verify proves that the checksums file came from this project's release
// workflow, which is what makes the digest inside it worth checking against.
//
// A digest served beside the file it describes proves the download arrived
// intact and says nothing about who made it: anything that could serve one
// could serve both. The signature is what closes that, and Dibs publishes a
// cosign bundle over the checksums file for exactly this.
//
// It refuses rather than warning when cosign is missing. This step decides
// which binary this machine is about to run as a daemon, and "proceed, but
// print a line saying it was not checked" is a line that scrolls past: the
// pre-release review has already caught two guarantees in this repository
// that were documented, printed and not enforced. The refusal names the one
// command that fixes it, and --allow-unsigned is there for a machine that
// genuinely cannot have it, typed by a person who has read why.
func Verify(ctx context.Context, c *http.Client, rel Release, dir string) (string, error) {
	cosign, err := usableCosign(ctx)
	if err != nil {
		return "", err
	}
	checksums := filepath.Join(dir, ChecksumsName)
	bundle := filepath.Join(dir, BundleName)
	if err := saveTo(ctx, c, DownloadURL(rel.Tag, ChecksumsName), checksums); err != nil {
		return "", err
	}
	if err := saveTo(ctx, c, DownloadURL(rel.Tag, BundleName), bundle); err != nil {
		return "", fmt.Errorf("fetching the signature bundle for %s: %w", rel.Tag, err)
	}
	// The identity is the WORKFLOW at the TAG, not merely "somebody at
	// Agenxy": a signature is only worth the identity it is bound to, and
	// accepting any certificate this repository has ever produced would accept
	// one from a pull request that never released anything.
	identity := "https://github.com/" + Repo + "/.github/workflows/release.yml@refs/tags/" + rel.Tag
	// #nosec G204 -- no shell; every argument is built here from constants and
	// the tag this release reported, never from caller-supplied text.
	out, err := exec.CommandContext(ctx, cosign, "verify-blob", checksums,
		"--bundle", bundle,
		"--certificate-identity", identity,
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("the signature over %s did not verify for %s, so this is not "+
			"the release %s published and nothing has been installed:\n%s",
			ChecksumsName, rel.Tag, Repo, strings.TrimSpace(string(out)))
	}
	// Read AFTER the signature is checked, and returned so the caller reads
	// the digest out of exactly what was proved.
	proved, err := os.ReadFile(checksums) // #nosec G304 -- written by saveTo, in the caller's temp dir
	if err != nil {
		return "", err
	}
	return string(proved), nil
}

// usableCosign finds a cosign that can actually run.
//
// Finding the NAME on PATH is not the same as having the tool, and the
// difference matters here more than almost anywhere: this machine has a mise
// shim called `cosign` with no version selected, so `exec.LookPath` succeeds,
// the verify call fails, and the honest report of that is "the signature
// could not be checked" while the code said "the signature did not verify",
// which accuses the release of being forged. A check that cannot run and a
// check that fails are different answers and an operator acts on them
// differently: one is "install this", the other is "do not run this binary".
//
// So the tool is asked its version first. That is the cheapest call it has,
// it is made once per fetch, and it turns an unusable shim into the sentence
// that names it.
func usableCosign(ctx context.Context) (string, error) {
	path, err := exec.LookPath("cosign")
	if err != nil {
		return "", errors.New("cosign is not installed, so the release's signature cannot " +
			"be checked, and a checksum served beside the file it describes proves only " +
			"that the download arrived intact. `brew install cosign`, then run this " +
			"again; `--allow-unsigned` accepts the digest alone if this machine cannot " +
			"have it")
	}
	// #nosec G204 -- no shell; the path is whatever LookPath resolved.
	if out, err := exec.CommandContext(ctx, path, "version").CombinedOutput(); err != nil {
		return "", fmt.Errorf("cosign is on PATH at %s but will not run, so the release's "+
			"signature CANNOT BE CHECKED: this is not a failed verification, it is a "+
			"missing tool. Fix the install (a mise or asdf shim with no version selected "+
			"looks exactly like this), or pass `--allow-unsigned` to accept the digest "+
			"alone.\n\n%s", path, strings.TrimSpace(string(out)))
	}
	return path, nil
}

func getText(ctx context.Context, c *http.Client, url string) (string, error) {
	body, err := fetchBody(ctx, c, url)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	b, err := io.ReadAll(io.LimitReader(body, 1<<20))
	return string(b), err
}

func saveTo(ctx context.Context, c *http.Client, url, path string) error {
	_, err := download(ctx, c, url, path)
	return err
}

// download writes one asset to path and returns its sha256, computed from the
// bytes as they land rather than by reading the file back: the digest has to
// be of what was written, and a second read is a second chance to disagree.
func download(ctx context.Context, c *http.Client, url, path string) (string, error) {
	body, err := fetchBody(ctx, c, url)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- caller's own temp dir
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(body, maxAsset))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n >= maxAsset {
		return "", fmt.Errorf("%s is larger than %d bytes, which is not a Dibs release", url, maxAsset)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func fetchBody(ctx context.Context, c *http.Client, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return resp.Body, nil
}

// extract unpacks the payload of a release archive into dir.
//
// Only the payload: the archive also carries the licence, the readme, the
// changelog and two man pages, and an upgrade is not the thing that should
// scatter those next to a binary.
//
// Every path is checked against the directory it is being written into. A tar
// entry names its own destination, so an archive is a list of paths chosen by
// whoever made it, and `../../..` in one of them is how an unpacker writes
// outside the directory it was told to use. This one is our own signed
// release, which is exactly the reasoning that makes a check feel unnecessary
// and is not a property of the code.
func extract(archive, dir string) error {
	f, err := os.Open(archive) // #nosec G304 -- written by download, in the caller's temp dir
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s is not a gzip archive: %w", filepath.Base(archive), err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var written int
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if !wanted(h.Name) {
			continue
		}
		isFile, err := unpack(tr, h, safeJoin(dir, h.Name))
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(archive), err)
		}
		if isFile {
			written++
		}
	}
	if written == 0 {
		return fmt.Errorf("%s carried none of %v", filepath.Base(archive), payload)
	}
	return nil
}

// wanted reports whether a tar entry belongs to the payload: a payload name
// itself, or something inside a payload directory.
func wanted(name string) bool {
	clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(name)), "./")
	for _, p := range payload {
		if clean == p || strings.HasPrefix(clean, p+"/") {
			return true
		}
	}
	return false
}

// safeJoin resolves a tar entry's name inside dir.
//
// The ROOTING is what does the work: `filepath.Clean("/" + name)` cannot
// produce a path that climbs above its own root, so joining the result onto
// dir always lands inside dir, whatever the archive asked for.
//
// Nothing that reaches here today could escape anyway, because `wanted` has
// already cleaned the name and required it to be a payload path, and a
// cleaned path under `Dibs.app/` has no way back out. That is a property of
// the payload filter and not of this function, and the two are worth keeping
// separate: the filter exists to leave documentation out of an install, and
// it would be an unpleasant surprise for path safety to leave with it the day
// somebody widens it. So this stays, tested directly rather than through
// extract, because through extract it is unreachable and a test that cannot
// fail is a test that says nothing. Mutation testing is how that was noticed:
// the first version of the test passed with the rooting removed.
func safeJoin(dir, name string) string {
	return filepath.Join(dir, filepath.Clean("/"+name))
}

// unpack writes one entry, reporting whether it was a file rather than a
// directory so the caller can tell an archive that carried nothing from one
// that carried only empty directories.
func unpack(r io.Reader, h *tar.Header, target string) (bool, error) {
	switch h.Typeflag {
	case tar.TypeDir:
		return false, os.MkdirAll(target, 0o755) // #nosec G301 -- an app bundle is read by the system
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { // #nosec G301
			return false, err
		}
		return true, writeFile(r, target, h.FileInfo().Mode().Perm())
	default:
		// A symlink or a device node in a release archive is not something
		// this has ever published, so it is not something to interpret.
		return false, fmt.Errorf("carries %q, which is neither a file nor a directory; "+
			"a Dibs release contains only those", h.Name)
	}
}

func writeFile(r io.Reader, target string, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode) // #nosec G304 -- checked by safeJoin
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(r, maxAsset))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
