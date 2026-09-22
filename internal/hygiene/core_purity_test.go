package hygiene

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// internal/core imports nothing of this project's, and nothing that
// carries state.
//
// AGENTS.md rule 1 states the first half: core is the pure fold, no I/O,
// no clock, no randomness, no network. This checks it, and it checks the
// consequence that took this repository three releases to notice.
//
// The consequence is about DIRECTION. Because core imports nothing, every
// other package can import core, and none of them has to keep a copy of a
// rule core already states. Three did anyway, each with a comment saying
// so in the same words: `paths.Portable` hand-copied the UNC cleaning with
// "core may not import this package; the two must stay identical", and
// `internal/overlap` kept two literal bounds with "overlap sits below core
// and does not import it", guarded by a test that read overlap's source to
// check the numbers still matched. Both premises were true about the
// direction they named and false about the one that mattered. The copies
// are gone; this is what keeps the reasoning that removed them true.
//
// So a failure here is not only "core stopped being pure". It is also
// "somebody is about to be told they have to copy a rule out of core", and
// the answer to that is to move the impure part out, not to copy.
func TestCoreImportsNothingThatCouldMakeItImpure(t *testing.T) {
	// The standard library a pure fold may use: values, strings, numbers,
	// sorting, errors. Anything that reaches the disk, the clock, the
	// network or the process is not here on purpose. `time` IS here,
	// because core handles time.Time values it is HANDED; the calls that
	// would make it read a clock instead are caught by
	// TestCoreReadsNoClockAndNoRandomness below, which is a separate test
	// because an import list cannot see a call.
	allowed := map[string]bool{
		"bytes": true, "cmp": true, "crypto/sha256": true,
		"encoding/binary": true, "encoding/hex": true,
		"encoding/json": true, "errors": true, "fmt": true, "maps": true,
		"math": true, "path": true, "regexp": true, "slices": true,
		"sort": true, "strconv": true, "strings": true, "time": true,
		"unicode": true, "unicode/utf8": true,
	}

	dir := filepath.Join("..", "core")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing internal/core/%s: %v", name, err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("internal/core/%s: unquoting %s: %v", name, imp.Path.Value, err)
			}
			switch {
			case strings.HasPrefix(p, "github.com/agenxy/dibs/"):
				t.Errorf("internal/core/%s imports %s. core imports nothing of this "+
					"project's, which is what lets every other package import IT rather "+
					"than keep a copy of a rule it states. An import here inverts that "+
					"and the copies come back.", name, p)
			case strings.Contains(p, "."):
				t.Errorf("internal/core/%s imports the third-party %s: the fold replays "+
					"ops written by older code, and a dependency is a decision somebody "+
					"else can change underneath a ledger", name, p)
			case !allowed[p]:
				t.Errorf("internal/core/%s imports %q, which is not on the pure list. If "+
					"it genuinely cannot make the fold depend on the disk, the clock, the "+
					"network or the process, add it here and say why; otherwise the impure "+
					"part belongs in the engine, recorded into the op.", name, p)
			}
		}
	}
	if files < 10 {
		t.Fatalf("read %d files in internal/core: this test is looking in the wrong place "+
			"and would pass on an empty directory", files)
	}
}

// And core calls nothing that would make the fold's answer depend on when
// it ran.
//
// The import guard above cannot see this, and said it could. It allows
// `time`, deliberately, because the fold is HANDED time.Time values and
// stores them; what it must never do is read the clock itself. The review
// that caught the overclaim also found the instance: `ReattachBySessionIDForTest`
// called time.Now() in a non-test file of the package, which nothing
// replays through and which is exactly the precedent rule 1 exists to
// refuse. Round fifty-eight of the pre-release review, in the guard's own
// round.
//
// A call, not an import, so this parses the whole file. Test files are
// excluded for the same reason as above: a fixture may read a clock to
// build an input, and handing the fold a value is the permitted thing.
func TestCoreReadsNoClockAndNoRandomness(t *testing.T) {
	// package.Function pairs that answer differently on two runs over the
	// same ledger, which is what state == fold(ledger) forbids.
	banned := map[string]map[string]string{
		"time": {
			"Now":   "reads the wall clock; the caller hands the fold its `now`",
			"Since": "reads the wall clock",
			"Until": "reads the wall clock",
			"Tick":  "is a clock and a goroutine",
			"After": "is a clock and a goroutine",
		},
		"rand": {"": "is randomness; a decision the fold makes must be the same on replay"},
		"os":   {"": "is the process and its environment"},
	}

	dir := filepath.Join("..", "core")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing internal/core/%s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			fns, watched := banned[pkg.Name]
			if !watched {
				return true
			}
			why, named := fns[sel.Sel.Name]
			if !named {
				if w, all := fns[""]; all {
					why = w
				} else {
					return true
				}
			}
			at := fset.Position(sel.Pos())
			t.Errorf("internal/core/%s:%d calls %s.%s, which %s. The fold replays "+
				"ops written by older code and must reach the same state every time; "+
				"an impure input belongs in the Op, recorded at ingress by the engine.",
				name, at.Line, pkg.Name, sel.Sel.Name, why)
			return true
		})
	}
	if files < 10 {
		t.Fatalf("read %d files in internal/core: this test is looking in the wrong place "+
			"and would pass on an empty directory", files)
	}
}
