// Command archivecheck opens a built release archive and asserts that every
// path the runtime resolves is inside it, at exactly that path.
//
// WHY THIS OPENS THE TAR. Two guards already watched the bundled helpers and
// both read `.goreleaser.yml`. That is a check on the configuration's spelling,
// not on the artifact, and the difference is not academic: the notifier bundle
// shipped as `Dibs.app/MacOS/dibs-notify` for a whole release cycle because a
// `src: Dibs.app/**/*` glob ate the `Contents` level, and the guard looking for
// the substring `src: Dibs.app` found it in that very line and passed. The
// archive was not a bundle macOS recognises and was not where internal/notify
// looks, so `dibs doctor` from an extracted archive said the notifier was not
// installed: the failure the round that added the entry believed it had closed,
// reported by the product about itself, past a green gate.
//
// The expected paths are READ FROM THE SOURCE, not listed here. A helper added
// tomorrow is covered on the day it is added rather than on the day somebody
// remembers this program.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// helperConst finds `helperName = "..."` in a package's Go source. Both
// runtime helpers declare their path that way, and internal/hygiene already
// reads the presence one the same way.
var helperConst = regexp.MustCompile(`helperName\s*=\s*"([^"]+)"`)

// packages that resolve a file beside the executable.
var runtimeHelpers = []string{"internal/humanauth", "internal/notify"}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "archivecheck:", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}

	want, err := expectedPaths(root)
	if err != nil {
		return err
	}
	// The two binaries are the archive's whole reason to exist, and an archive
	// that lost one is not something this should discover by inference.
	want = append(want, "dibd", "dibs")
	sort.Strings(want)

	// EVERY darwin archive, however many that is.
	//
	// One today, because the Mac Intel target was dropped. Written as a glob
	// rather than a name so that adding a target adds a check instead of
	// silently leaving one unexamined, which is the shape of the defect that
	// prompted the architecture check below: the bundled notifier was built once
	// on the release host and copied into both archives, so it was arm64-only
	// inside darwin_amd64 and this program, reading only the arm64 tarball, was
	// green over it.
	archives, err := filepath.Glob(filepath.Join(root, "dist", "*_darwin_*.tar.gz"))
	if err != nil {
		return err
	}
	if len(archives) == 0 {
		return fmt.Errorf("found no darwin archive under dist/.\n" +
			"Build one first: goreleaser release --snapshot --clean")
	}
	sort.Strings(archives)

	for _, a := range archives {
		if err := carries(a, want); err != nil {
			return err
		}
	}
	fmt.Printf("archivecheck: %d darwin archive(s) carry all %d runtime paths, each "+
		"runnable on the Mac it is for\n", len(archives), len(want))
	return nil
}

// carries proves one archive holds every runtime path, and that the executables
// among them run on the machine this archive is for.
func carries(path string, want []string) error {
	have, err := entries(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if len(have) == 0 {
		return fmt.Errorf("%s contains no files, so this check verified nothing", path)
	}

	var missing []string
	for _, w := range want {
		if _, ok := have[w]; !ok {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		var listed []string
		for p := range have {
			listed = append(listed, p)
		}
		sort.Strings(listed)
		return fmt.Errorf("%s is missing %d path(s) the runtime resolves:\n  %s\n\n"+
			"What the archive actually carries:\n  %s\n\n"+
			"A path that is present under a DIFFERENT name is the failure this exists to "+
			"catch: the file is in the archive, every guard that reads .goreleaser.yml is "+
			"green, and the product reports the component missing to the person who "+
			"downloaded it",
			filepath.Base(path), len(missing),
			strings.Join(missing, "\n  "), strings.Join(listed, "\n  "))
	}

	// AND THE ARCHITECTURE, because a file at the right path that cannot execute
	// is not a working installation. The notifier returns its exec error rather
	// than falling back, so on the wrong Mac it fails silently to a person who
	// was supposed to be notified.
	arch := archArchitecture(path)
	if arch == "" {
		return fmt.Errorf("%s: cannot tell which architecture this archive is for "+
			"from its name, so the executables in it are unchecked", filepath.Base(path))
	}
	for _, w := range want {
		body := have[w]
		if !isMachO(body) {
			continue // documentation, an icon, a plist
		}
		archs, aerr := machoArchs(body)
		if aerr != nil {
			return fmt.Errorf("%s: %s: %w", filepath.Base(path), w, aerr)
		}
		if !slices.Contains(archs, arch) {
			return fmt.Errorf("%s is the %s archive and its %s is built for %v.\n\n"+
				"It cannot run on the Mac this archive is for. A helper built once on "+
				"the release host and copied into every archive looks correct in the "+
				"file listing and is inert on half the machines that download it",
				filepath.Base(path), arch, w, archs)
		}
	}
	return nil
}

// archArchitecture reads the target out of a GoReleaser archive name.
func archArchitecture(path string) string {
	switch name := filepath.Base(path); {
	case strings.Contains(name, "_darwin_arm64"):
		return "arm64"
	case strings.Contains(name, "_darwin_amd64"):
		return "amd64"
	}
	return ""
}

// isMachO reports whether these bytes are a Mach-O image, thin or fat.
func isMachO(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	switch binary.BigEndian.Uint32(b[:4]) {
	case 0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe, // thin, both endians
		macho.MagicFat, 0xbebafeca: // fat, both endians
		return true
	}
	return false
}

// machoArchs names the architectures an image contains, in Go's spelling so it
// can be compared with an archive's own suffix.
func machoArchs(b []byte) ([]string, error) {
	r := bytes.NewReader(b)
	if fat, err := macho.NewFatFile(r); err == nil {
		var out []string
		for _, a := range fat.Arches {
			out = append(out, goArch(a.Cpu))
		}
		return out, nil
	}
	f, err := macho.NewFile(r)
	if err != nil {
		return nil, fmt.Errorf("not a readable Mach-O image: %w", err)
	}
	return []string{goArch(f.Cpu)}, nil
}

func goArch(c macho.Cpu) string {
	switch c {
	case macho.CpuAmd64:
		return "amd64"
	case macho.CpuArm64:
		return "arm64"
	// The release targets darwin/arm64 and nothing else on a Mac. A helper built
	// for any of these is wrong wherever it turns up, and naming them keeps the
	// mismatch readable in the error rather than as a number.
	case macho.Cpu386, macho.CpuArm, macho.CpuPpc, macho.CpuPpc64:
		return c.String()
	}
	return c.String()
}

// expectedPaths reads each runtime helper's declared location from its package.
func expectedPaths(root string) ([]string, error) {
	var out []string
	for _, pkg := range runtimeHelpers {
		dir := filepath.Join(root, pkg)
		names, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		var found string
		for _, e := range names {
			if e.IsDir() || filepath.Ext(e.Name()) != ".go" ||
				strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			// #nosec G304 -- a .go file in a fixed package of this repository
			b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
			if rerr != nil {
				return nil, rerr
			}
			if m := helperConst.FindSubmatch(b); m != nil {
				found = string(m[1])
				break
			}
		}
		if found == "" {
			return nil, fmt.Errorf("%s declares no helperName, so this check cannot see "+
				"what that package looks for beside the executable. Either it stopped "+
				"resolving one, and belongs off the list, or it spells it differently now "+
				"and this guard has gone blind", pkg)
		}
		out = append(out, found)
	}
	return out, nil
}

// entries reads the archive into path → contents. The bytes are needed because
// the check is not only that a file is present but that it can run.
func entries(path string) (map[string][]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- a path this program globbed under dist/
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()

	out := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		// The header only, for anything too big to be a helper: this reads a
		// release archive, and holding all of it would be pointless.
		lim := io.LimitReader(tr, 64<<20)
		body, rerr := io.ReadAll(lim)
		if rerr != nil {
			return nil, fmt.Errorf("%s: %w", h.Name, rerr)
		}
		out[filepath.Clean(h.Name)] = body
	}
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
