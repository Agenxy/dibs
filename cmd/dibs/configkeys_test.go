package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// knownConfigKeys is a copy of dibd's top-level configuration keys, kept
// because cmd/dibd is package main and cannot be imported.
//
// A copy is a liability the moment it drifts: a key added to the daemon would
// be reported here as one the daemon does not know, and the CLI would refuse a
// configuration that is perfectly valid. This reads the struct out of the
// daemon's source, so the list cannot go stale without failing.
func TestConfigKeysMatchTheDaemon(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../dibd/config.go", nil, 0)
	if err != nil {
		t.Fatalf("cannot read the daemon's config: %v", err)
	}

	want := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Config" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fld := range st.Fields.List {
			if fld.Tag == nil {
				continue
			}
			tag := reflect.StructTag(strings.Trim(fld.Tag.Value, "`"))
			if k := tag.Get("toml"); k != "" && k != "-" {
				want[strings.Split(k, ",")[0]] = true
			}
		}
		return false
	})

	if len(want) == 0 {
		t.Fatal("no toml keys found on dibd's Config: this probe is reading nothing")
	}
	for k := range want {
		if !knownConfigKeys[k] {
			t.Errorf("dibd accepts %q and this CLI would refuse a config that sets it", k)
		}
	}
	for k := range knownConfigKeys {
		if !want[k] {
			t.Errorf("%q is no longer a dibd config key, so this list is stale", k)
		}
	}
}
