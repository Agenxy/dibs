package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Copied from a real release (v0.0.7), because the format is the thing under
// test: two spaces between the digest and the name, and the assets of every
// platform in one file.
const realChecksums = `4e1fedac0370b4d60ca29d0c8b3e24adfeef2736046e666a324e67877bbd3f0b  dibs_0.0.7_darwin_arm64.tar.gz
025f33439ecd6ae8eaf27b7525b58c4763aabfeeaf5a1d667e72cb233e139de1  dibs_0.0.7_darwin_arm64.tar.gz.sbom.json
f87de1c85ddc7aa122330e53aa7e3cebc4c5a90c7f23495822e74d102ca6cf25  dibs_0.0.7_linux_amd64.tar.gz
`

func TestTheDigestIsReadForTheAssetAsked(t *testing.T) {
	got, err := ChecksumFor(realChecksums, "dibs_0.0.7_darwin_arm64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "4e1fedac0370b4d60ca29d0c8b3e24adfeef2736046e666a324e67877bbd3f0b" {
		t.Fatalf("read the wrong line: %s", got)
	}
}

// The sbom's name has the archive's name as a PREFIX, so a loose match reads
// the wrong digest and refuses a download that is perfectly good.
//
// The order in the file decides whether that is visible, which is why this
// does not reuse the fixture above: GoReleaser writes the archive first, so a
// prefix match happens to find the right line there and the bug hides. Here
// the sbom comes first, which is the arrangement a future GoReleaser is
// entitled to produce.
func TestAnAssetNameIsMatchedWholeAndNotAsAPrefix(t *testing.T) {
	const sbomFirst = `aaaa111111111111111111111111111111111111111111111111111111111111  dibs_0.0.7_darwin_arm64.tar.gz.sbom.json
bbbb222222222222222222222222222222222222222222222222222222222222  dibs_0.0.7_darwin_arm64.tar.gz
`
	got, err := ChecksumFor(sbomFirst, "dibs_0.0.7_darwin_arm64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "bbbb222222222222222222222222222222222222222222222222222222222222" {
		t.Fatalf("read the sbom's digest for the archive: %s", got)
	}
}

// An asset with no line is an ERROR and never an empty digest. A caller that
// compared a download against "" would accept anything and report that it had
// verified it, which is the shape of check that checks nothing.
func TestAnUnlistedAssetIsAnErrorRatherThanAnEmptyDigest(t *testing.T) {
	got, err := ChecksumFor(realChecksums, "dibs_0.0.7_windows_amd64.tar.gz")
	if err == nil {
		t.Fatalf("an asset the file does not name returned %q and no error", got)
	}
	if got != "" {
		t.Fatalf("an error must not also return a digest, got %q", got)
	}
}

func TestATruncatedDigestIsRefused(t *testing.T) {
	if _, err := ChecksumFor("dead  dibs_1.0.0_linux_arm64.tar.gz\n",
		"dibs_1.0.0_linux_arm64.tar.gz"); err == nil {
		t.Fatal("four hex characters is not a sha256 and must not be accepted as one")
	}
}

// The one platform an operator is likely to be on and Dibs does not publish.
// Naming it beats "no asset found": the answer is that there is no such build
// and there is another way to install.
func TestMacIntelIsToldWhatToDoInsteadOfNotFound(t *testing.T) {
	_, err := ArchiveName("0.0.8", "darwin", "amd64")
	if err == nil {
		t.Fatal("dibs publishes no macOS Intel build")
	}
	if !strings.Contains(err.Error(), "source") {
		t.Errorf("the refusal has to name the way that works; got %q", err)
	}
	for _, c := range []struct{ goos, goarch, want string }{
		{"darwin", "arm64", "dibs_0.0.8_darwin_arm64.tar.gz"},
		{"linux", "amd64", "dibs_0.0.8_linux_amd64.tar.gz"},
		{"linux", "arm64", "dibs_0.0.8_linux_arm64.tar.gz"},
	} {
		got, err := ArchiveName("0.0.8", c.goos, c.goarch)
		if err != nil || got != c.want {
			t.Errorf("ArchiveName(%s/%s) = %q, %v; want %q", c.goos, c.goarch, got, err, c.want)
		}
	}
}

// A build with no release behind it is not out of date. `internal/build`
// reports a bare revision for that case rather than a number, and telling a
// developer their own working build is stale on every check is how a check
// gets ignored.
func TestABuildThatCannotBePlacedIsNotCalledStale(t *testing.T) {
	for _, have := range []string{"devel", "ed07e4883c86", "ed07e4883c86-dirty"} {
		if got := Compare(have, "0.0.9"); got != Unknowable {
			t.Errorf("Compare(%q, 0.0.9) = %v, want Unknowable", have, got)
		}
	}
	if got := Compare("0.0.8", "0.0.9"); got != Behind {
		t.Errorf("0.0.8 against a published 0.0.9 is Behind, got %v", got)
	}
	if got := Compare("0.0.9", "0.0.9"); got != Current {
		t.Errorf("the published version is Current, got %v", got)
	}
	// Between `task release` and the tag, main carries a version nothing has
	// published. That is not an update to offer.
	if got := Compare("0.0.9", "0.0.8"); got != Ahead {
		t.Errorf("a stamped but untagged build is Ahead, got %v", got)
	}
	// And the case that made the comparator worth writing: a source build
	// reports a pseudo-version, which is BELOW the release it names.
	if got := Compare("0.0.8-0.20260922110423-ed07e4883c86", "0.0.8"); got != Behind {
		t.Errorf("a source build before 0.0.8 is Behind the published 0.0.8, got %v", got)
	}
}

// A brew install is never self-replaced. Overwriting a file brew owns leaves
// its records and the disk disagreeing, and the next `brew upgrade` puts the
// old build back with nothing explaining why.
func TestAHomebrewInstallIsLeftToHomebrew(t *testing.T) {
	for _, p := range []string{
		"/opt/homebrew/bin/dibd",
		"/usr/local/Caskroom/dibs/0.0.7/dibd",
		"/home/linuxbrew/.linuxbrew/bin/dibd",
	} {
		if by := Managed(p); by != "homebrew" {
			t.Errorf("%s is a homebrew install, got %q", p, by)
		}
	}
	for _, p := range []string{"/Users/lael/.local/bin/dibd", "/usr/bin/dibd", "/opt/dibs/dibd"} {
		if by := Managed(p); by != "" {
			t.Errorf("%s is not managed by anything, got %q", p, by)
		}
	}
	if ManagedFix("homebrew") != "brew upgrade --cask dibs" {
		t.Error("the refusal has to carry the command that works")
	}
}

func TestLatestReadsTheTagAndDropsTheV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.1.2","html_url":"https://example.invalid/r"}`))
	}))
	defer srv.Close()
	rel, err := latestFrom(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "0.1.2" || rel.Tag != "v0.1.2" {
		t.Fatalf("got %+v", rel)
	}
}

// Rate limiting is reported as itself. An operator told "you are up to date"
// when nothing was checked is worse off than one told the check did not
// happen, and this is the failure a machine behind a shared address meets.
func TestRateLimitingIsNotSilence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := latestFrom(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("a rate-limited check must be an error, not an absence of news")
	}
	if !strings.Contains(err.Error(), "rate limiting") {
		t.Errorf("the error has to say what happened; got %q", err)
	}
}

// The end to end shape, against a server that serves a real archive: a
// download whose digest does not match the published one installs nothing.
func TestADownloadThatDoesNotMatchTheReleaseIsRefused(t *testing.T) {
	archive := tarball(t, map[string]string{"dibd": "the real thing", "dibs": "cli"})
	var serveChecksums string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ChecksumsName):
			_, _ = w.Write([]byte(serveChecksums))
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			_, _ = w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	rel := Release{Version: "9.9.9", Tag: "v9.9.9"}
	asset, err := ArchiveName(rel.Version, "linux", "arm64")
	if err != nil {
		t.Fatal(err)
	}

	// A digest for a DIFFERENT payload: the download is intact and is not the
	// release.
	serveChecksums = fmt.Sprintf("%s  %s\n", strings.Repeat("0", 64), asset)
	err = fetchFrom(context.Background(), srv.Client(), rel, "linux", "arm64", t.TempDir(), srv.URL)
	if err == nil {
		t.Fatal("a download whose digest does not match must be refused")
	}
	if !strings.Contains(err.Error(), "nothing has been installed") {
		t.Errorf("the refusal has to say the machine was not changed; got %q", err)
	}

	// And the same archive with its real digest unpacks the payload.
	sum := sha256.Sum256(archive)
	serveChecksums = fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)
	dest := t.TempDir()
	if err := fetchFrom(context.Background(), srv.Client(), rel, "linux", "arm64", dest, srv.URL); err != nil {
		t.Fatalf("a matching download should unpack: %v", err)
	}
	for _, name := range []string{"dibd", "dibs"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("%s was not unpacked: %v", name, err)
		}
	}
}

// A tar entry names its own destination, so an archive is a list of paths
// chosen by whoever built it, and `../../..` in one of them is how an
// unpacker writes outside the directory it was told to use. This one is our
// own signed release, which is exactly the reasoning that makes the check
// feel unnecessary and is not a property of the code.
//
// It asserts the PROPERTY, not the line that enforces it. The first version
// of this asserted an error and passed with the guard deleted, because the
// payload filter rejected the entry first for an unrelated reason: mutation
// testing is the only thing that showed the difference.
func TestNothingIsWrittenOutsideTheDestination(t *testing.T) {
	// Both shapes: one that would escape by climbing, and one that would
	// escape while still looking like payload.
	archive := tarball(t, map[string]string{
		"../escaped":              "no",
		"dibd/../../also-escaped": "no",
		"Dibs.app/../../third":    "no",
		"dibd":                    "the real thing",
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	// A destination NESTED inside a directory that is otherwise empty, so
	// anything that lands beside it is visible.
	outer := t.TempDir()
	dest := filepath.Join(outer, "inside")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extract(path, dest); err != nil {
		t.Fatalf("the archive also carries a real payload, so it should unpack: %v", err)
	}
	left, err := os.ReadDir(outer)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range left {
		if e.Name() != "inside" {
			t.Errorf("%q was written outside the destination directory", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "dibd")); err != nil {
		t.Errorf("the real payload was not unpacked: %v", err)
	}
}

// And the function that resolves a destination keeps it inside, on its own.
//
// Through extract this is unreachable: the payload filter rejects every name
// below before it gets here. That is what makes a direct test the honest one
// rather than a redundant one, and the reason this function keeps the rooting
// at all is that the filter is not there for safety and could be widened.
func TestAResolvedDestinationIsAlwaysInsideTheDirectory(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "tmp", "dest")
	for _, name := range []string{
		"../escaped",
		"../../escaped",
		"dibd/../../escaped",
		"/etc/passwd",
		"./../../escaped",
	} {
		got := safeJoin(dir, name)
		if !strings.HasPrefix(got, dir+string(filepath.Separator)) {
			t.Errorf("safeJoin(%q, %q) = %q, which is outside %s", dir, name, got, dir)
		}
	}
}

// The archive carries the licence, the readme, the changelog and two man
// pages beside the binaries. An upgrade is not the thing that should scatter
// those next to a daemon.
func TestOnlyThePayloadIsUnpacked(t *testing.T) {
	archive := tarball(t, map[string]string{
		"dibd": "d", "dibs": "c", "README.md": "no", "dibs.1": "no",
		"Dibs.app/Contents/MacOS/dibs-notify": "n",
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := extract(path, dest); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dibd", "dibs", "Dibs.app/Contents/MacOS/dibs-notify"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(want))); err != nil {
			t.Errorf("%s belongs to the payload and was not unpacked", want)
		}
	}
	for _, unwanted := range []string{"README.md", "dibs.1"} {
		if _, err := os.Stat(filepath.Join(dest, unwanted)); err == nil {
			t.Errorf("%s is documentation and was unpacked beside the daemon", unwanted)
		}
	}
}

func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf strings.Builder
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return []byte(buf.String())
}
