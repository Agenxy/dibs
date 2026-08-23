// fetchpinned downloads one file, checks it against a pinned SHA-256, and
// extracts a single named entry from the tarball.
//
// Replaces a `run: |` block that did the same with curl, sha256sum, tar and a
// pipe. This repository does not use shell for build or release steps, and a
// workflow's embedded script is a shell script that happens to live in YAML.
// The shell version also depended on `set -euo pipefail` being remembered: the
// checksum ran in a pipeline, so without `pipefail` a failed verification would
// have been the exit status of `echo`.
//
// Verification happens before anything is written to disk in executable form,
// and the digest is compared in full rather than by prefix.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	url := flag.String("url", "", "what to download")
	want := flag.String("sha256", "", "the digest it must have, hex")
	entry := flag.String("extract", "", "the single tar entry to write out")
	mode := flag.Int("mode", 0o755, "file mode for the extracted entry")
	flag.Parse()
	// #nosec G115 -- a file mode from this repository's own workflow
	if err := run(*url, *want, *entry, os.FileMode(*mode)); err != nil {
		fmt.Fprintln(os.Stderr, "fetchpinned:", err)
		os.Exit(1)
	}
}

func run(url, want, entry string, mode os.FileMode) error {
	switch {
	case url == "":
		return errors.New("-url is required")
	case want == "":
		return errors.New("-sha256 is required: an unpinned download is not a pin")
	case entry == "":
		return errors.New("-extract is required")
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url) // #nosec G107 -- the url is a workflow constant
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, want) {
		return fmt.Errorf("%s hashes to %s, not the pinned %s: refusing to use it",
			url, got, want)
	}
	return extract(body, entry, mode, url)
}

// extract writes one named entry out of a gzipped tar, and nothing else.
func extract(archive []byte, entry string, mode os.FileMode, from string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s contains no entry named %q", from, entry)
		}
		if err != nil {
			return err
		}
		if filepath.Clean(h.Name) != filepath.Clean(entry) {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("%s in %s is not a regular file", entry, from)
		}
		return writeEntry(filepath.Base(entry), tr, mode)
	}
}

func writeEntry(name string, r io.Reader, mode os.FileMode) error {
	// #nosec G304 -- filepath.Base of the -extract flag, into the working
	// directory: the flag is a constant in this repository's own workflow.
	out, err := os.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	// Bounded: a tar entry declares its own size, and an archive that lies
	// about it is exactly what a decompression bomb relies on.
	if _, err := io.Copy(out, io.LimitReader(r, 256<<20)); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
