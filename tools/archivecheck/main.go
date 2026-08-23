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
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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

	archives, err := filepath.Glob(filepath.Join(root, "dist", "*_darwin_arm64.tar.gz"))
	if err != nil {
		return err
	}
	if len(archives) != 1 {
		return fmt.Errorf("expected exactly one darwin_arm64 archive under dist/, found %d %v.\n"+
			"Build one first: goreleaser release --snapshot --clean", len(archives), archives)
	}

	have, err := entries(archives[0])
	if err != nil {
		return fmt.Errorf("%s: %w", archives[0], err)
	}
	if len(have) == 0 {
		return fmt.Errorf("%s contains no files, so this check verified nothing", archives[0])
	}

	var missing []string
	for _, w := range want {
		if !have[w] {
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
			filepath.Base(archives[0]), len(missing),
			strings.Join(missing, "\n  "), strings.Join(listed, "\n  "))
	}

	fmt.Printf("archivecheck: %s carries all %d runtime paths\n",
		filepath.Base(archives[0]), len(want))
	return nil
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

func entries(path string) (map[string]bool, error) {
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

	out := map[string]bool{}
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
		out[filepath.Clean(h.Name)] = true
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
